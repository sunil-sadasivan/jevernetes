use std::{
    io::Write,
    process::{Command, Stdio},
};
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
fn tui_rejects_json_stdin_and_noninteractive_output_before_collection() {
    for (args, expected) in [
        (vec!["k8s", "--tui", "--json"], "--json"),
        (vec!["files", "-", "--tui"], "stdin"),
        (vec!["k8s", "--tui"], "interactive terminal"),
    ] {
        let result = Command::new(env!("CARGO_BIN_EXE_jevernetes"))
            .args(args)
            .env_remove("TYPESAFE_API_KEY")
            .env_remove("TYPESAFEAI_API_KEY")
            .env_remove("TYPESAFE_API_KEY_FILE")
            .output()
            .unwrap();
        assert!(!result.status.success());
        assert!(String::from_utf8_lossy(&result.stderr).contains(expected));
        assert!(result.stdout.is_empty());
    }
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
