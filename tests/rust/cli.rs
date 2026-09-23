use std::{
    io::Write,
    process::{Command, Stdio},
};

#[test]
fn remote_validates_scope_and_does_not_require_provider_credentials() {
    let dir = tempfile::tempdir().unwrap();
    for (args, expected) in [
        (vec!["remote"], "--namespace"),
        (
            vec![
                "remote",
                "--namespace",
                "synthetic",
                "--incident",
                "../private",
            ],
            "64 hexadecimal",
        ),
        (
            vec![
                "remote",
                "--namespace",
                "synthetic",
                "--context",
                "missing",
                "--json",
            ],
            "Kubernetes configuration",
        ),
    ] {
        let result = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
            .args(args)
            .env("KUBECONFIG", dir.path().join("missing-kubeconfig"))
            .env_remove("TYPESAFE_API_KEY")
            .env_remove("TYPESAFEAI_API_KEY")
            .env_remove("TYPESAFE_API_KEY_FILE")
            .output()
            .unwrap();
        assert!(!result.status.success());
        let stderr = String::from_utf8_lossy(&result.stderr);
        assert!(stderr.contains(expected), "{stderr}");
        assert!(!stderr.contains("Set TYPESAFE"));
        assert!(result.stdout.is_empty());
    }
}
fn run(input: &[u8], extra: &[&str]) -> std::process::Output {
    let mut child = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .args(["files", "-", "--offline", "--json"])
        .args(extra)
        .env_remove("TYPESAFE_API_KEY")
        .env_remove("TYPESAFEAI_API_KEY")
        .env_remove("TYPESAFE_API_KEY_FILE")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    child.stdin.take().unwrap().write_all(input).unwrap();
    child.wait_with_output().unwrap()
}
#[test]
fn stdin_report_and_private_atomic_file() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("report.json");
    let result = run(
        b"ERROR failed password=synthetic-value\n  at frame\nINFO ready\n",
        &["--output", path.to_str().unwrap()],
    );
    assert!(result.status.success(), "{:?}", result.stderr);
    let v: serde_json::Value = serde_json::from_slice(&result.stdout).unwrap();
    assert_eq!(v["summary"]["events"], 2);
    assert_eq!(v["summary"]["important"], 1);
    assert_eq!(v["summary"]["uncertain"], 1);
    assert_eq!(v["events"][0]["line_count"], 2);
    assert!(!String::from_utf8_lossy(&result.stdout).contains("synthetic-value"));
    assert_eq!(
        v,
        serde_json::from_slice::<serde_json::Value>(&std::fs::read(&path).unwrap()).unwrap()
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(path).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }
}
#[test]
fn bounded_input_and_missing_file_are_partial() {
    let r = run(b"ERROR failed\nINFO ready\n", &["--max-file-bytes", "5"]);
    assert_eq!(r.status.code(), Some(2));
    let v: serde_json::Value = serde_json::from_slice(&r.stdout).unwrap();
    assert!(v["summary"]["coverage_gaps"].as_u64().unwrap() > 0);
    let r = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .args([
            "files",
            "/nonexistent-synthetic-fixture",
            "--offline",
            "--json",
        ])
        .output()
        .unwrap();
    assert_eq!(r.status.code(), Some(2));
}
#[test]
fn validation_and_missing_key_fail_before_collection() {
    let r = run(b"", &["--queue-size", "0"]);
    assert!(!r.status.success());
    let r = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .args(["k8s"])
        .env_remove("TYPESAFE_API_KEY")
        .env_remove("TYPESAFEAI_API_KEY")
        .env_remove("TYPESAFE_API_KEY_FILE")
        .output()
        .unwrap();
    assert_eq!(r.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&r.stderr).contains("--offline"));
}

#[test]
fn event_cap_is_global_and_exact_boundary_is_not_a_gap() {
    let r = run(b"one\ntwo\nthree\n", &["--max-events", "2"]);
    assert_eq!(r.status.code(), Some(2));
    let v: serde_json::Value = serde_json::from_slice(&r.stdout).unwrap();
    assert_eq!(v["summary"]["events"], 2);
    assert_eq!(v["events"].as_array().unwrap().len(), 2);
    let r = run(b"one\ntwo\n", &["--max-events", "2"]);
    assert!(r.status.success());
}
#[tokio::test]
async fn gzip_is_streamed_and_decompressed_cap_is_enforced() {
    use tokio::io::AsyncWriteExt;
    let directory = tempfile::tempdir().unwrap();
    let path = directory.path().join("synthetic.gz");
    let file = tokio::fs::File::create(&path).await.unwrap();
    let mut encoder = async_compression::tokio::write::GzipEncoder::new(file);
    encoder
        .write_all(b"ERROR synthetic\nINFO ready\n")
        .await
        .unwrap();
    encoder.shutdown().await.unwrap();
    drop(encoder);
    let run = |extra: &[&str]| {
        Command::new(env!("CARGO_BIN_EXE_jevernetes"))
            .args(["files", path.to_str().unwrap(), "--offline", "--json"])
            .args(extra)
            .output()
            .unwrap()
    };
    let result = run(&[]);
    assert!(result.status.success());
    let v: serde_json::Value = serde_json::from_slice(&result.stdout).unwrap();
    assert_eq!(v["summary"]["events"], 2);
    let result = run(&["--max-file-bytes", "5"]);
    assert_eq!(result.status.code(), Some(2));
    let v: serde_json::Value = serde_json::from_slice(&result.stdout).unwrap();
    assert_eq!(v["events"][0]["truncated"], true);
}
#[cfg(unix)]
#[test]
fn sigterm_writes_final_report_and_exits() {
    let directory = tempfile::tempdir().unwrap();
    let path = directory.path().join("final.json");
    let mut child = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .args([
            "files",
            "-",
            "--offline",
            "--json",
            "--output",
            path.to_str().unwrap(),
        ])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let mut input = child.stdin.take().unwrap();
    input.write_all(b"ERROR synthetic\nINFO pending\n").unwrap();
    // Exceed pipe capacity: completion proves the child has started reading,
    // after its signal handlers were installed. No startup sleep race.
    input.write_all(&vec![b'\n'; 2 * 1024 * 1024]).unwrap();
    input.flush().unwrap();
    assert!(
        Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status()
            .unwrap()
            .success()
    );
    let until = std::time::Instant::now() + std::time::Duration::from_secs(3);
    while child.try_wait().unwrap().is_none() {
        if std::time::Instant::now() > until {
            child.kill().unwrap();
            panic!("SIGTERM did not stop process");
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    drop(input);
    let result = child.wait_with_output().unwrap();
    assert_eq!(
        result.status.code(),
        Some(130),
        "status={:?}, stderr={}",
        result.status,
        String::from_utf8_lossy(&result.stderr)
    );
    let v: serde_json::Value = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    assert!(v["summary"]["coverage_gaps"].as_u64().unwrap() > 0);
}

#[test]
fn controller_cli_and_policy_validation_do_not_access_cluster() {
    let dir = tempfile::tempdir().unwrap();
    let policy = dir.path().join("policy.json");
    std::fs::write(&policy, r#"{"min_confidence":1.1}"#).unwrap();
    for extra in [
        vec!["--verdict-ttl", "0"],
        vec!["--sink", "email"],
        vec!["--no-grouping"],
        vec!["--json"],
        vec!["--webhook-url", "https://example.invalid"],
        vec!["--policy", policy.to_str().unwrap()],
    ] {
        let output = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
            .args([
                "controller",
                "--state",
                dir.path().join("db").to_str().unwrap(),
                "--namespace",
                "synthetic",
                "--offline",
            ])
            .args(extra)
            .output()
            .unwrap();
        assert!(!output.status.success());
        assert!(
            !dir.path().join("db").exists(),
            "invalid flags/policy must fail before state or cluster access"
        );
    }
    let help = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .args(["controller", "--help"])
        .output()
        .unwrap();
    assert!(help.status.success());
    assert!(String::from_utf8_lossy(&help.stdout).contains("--verdict-ttl"));
}

fn reject_controller_paths(state: &std::path::Path, output: &std::path::Path) {
    let result = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
        .env_clear()
        .args([
            "controller",
            "--namespace",
            "synthetic",
            "--offline",
            "--state",
        ])
        .arg(state)
        .arg("--output")
        .arg(output)
        .output()
        .unwrap();
    assert!(!result.status.success());
    assert!(
        String::from_utf8_lossy(&result.stderr).contains("Controller output conflicts with state"),
        "{}",
        String::from_utf8_lossy(&result.stderr)
    );
}

#[test]
fn controller_rejects_equal_paths_before_creating_state() {
    let dir = tempfile::tempdir().unwrap();
    let state = dir.path().join("state.db");
    reject_controller_paths(&state, &state);
    assert!(!state.exists());
    assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0);
}

#[test]
fn controller_rejects_normalized_paths_without_damaging_state() {
    let dir = tempfile::tempdir().unwrap();
    let state = dir.path().join("state.db");
    drop(jevernetes::controller::store::Store::open(&state).unwrap());
    let before = std::fs::read(&state).unwrap();
    let sub = dir.path().join("sub");
    std::fs::create_dir(&sub).unwrap();
    reject_controller_paths(&state, &sub.join(".././state.db"));
    assert_eq!(std::fs::read(&state).unwrap(), before);
}

#[cfg(unix)]
#[test]
fn controller_rejects_symlinks_hardlinks_and_sidecar_aliases() {
    use std::os::unix::fs::symlink;
    let dir = tempfile::tempdir().unwrap();
    let state = dir.path().join("state.db");
    let alias = dir.path().join("alias");
    symlink(&state, &alias).unwrap();
    // Dangling links must be resolved before state creation, in either direction.
    reject_controller_paths(&state, &alias);
    reject_controller_paths(&alias, &state);
    assert!(!state.exists());
    drop(jevernetes::controller::store::Store::open(&state).unwrap());
    let before = std::fs::read(&state).unwrap();
    reject_controller_paths(&state, &alias);
    reject_controller_paths(&alias, &state);
    let hardlink = dir.path().join("hardlink");
    std::fs::hard_link(&state, &hardlink).unwrap();
    reject_controller_paths(&state, &hardlink);
    let directory_alias = dir.path().join("directory-alias");
    symlink(dir.path(), &directory_alias).unwrap();
    reject_controller_paths(&state, &directory_alias.join("state.db"));
    for suffix in [".lock", "-journal", "-wal", "-shm"] {
        let sidecar = dir.path().join(format!("state.db{suffix}"));
        reject_controller_paths(&alias, &sidecar);
    }
    assert_eq!(std::fs::read(&state).unwrap(), before);
}
