//! Resolve report/state aliases without creating files. Directories must be trusted.
use super::store::Result;
use std::path::{Path, PathBuf};

// Canonicalize existing prefixes and follow dangling symlinks as well. Unlike lexical
// normalization, this preserves the meaning of `symlink/..`. Fail closed on loops,
// inaccessible paths, or unresolvable components; no filesystem mutations occur here.
fn resolve(path: &Path, depth: usize) -> Result<PathBuf> {
    if depth > 128 {
        return Err("Cannot resolve controller paths");
    }
    match std::fs::canonicalize(path) {
        Ok(path) => return Ok(path),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => (),
        Err(_) => return Err("Cannot resolve controller paths"),
    }
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    match std::fs::symlink_metadata(path) {
        Ok(meta) if meta.file_type().is_symlink() => {
            let target = std::fs::read_link(path).map_err(|_| "Cannot resolve controller paths")?;
            resolve(&parent.join(target), depth + 1)
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            let name = path.file_name().ok_or("Cannot resolve controller paths")?;
            Ok(resolve(parent, depth + 1)?.join(name))
        }
        _ => Err("Cannot resolve controller paths"),
    }
}

fn same_file(a: &Path, b: &Path) -> bool {
    if a == b {
        return true;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if let (Ok(a), Ok(b)) = (std::fs::metadata(a), std::fs::metadata(b)) {
            return a.dev() == b.dev() && a.ino() == b.ino();
        }
    }
    false
}

pub fn validate(state: &Path, output: &Path) -> Result<()> {
    let state = resolve(state, 0)?;
    let output = resolve(output, 0)?;
    // Replacing a SQLite journal or the lock inode can also destroy durability or
    // exclusive ownership, even when the database itself is not replaced.
    for suffix in ["", ".lock", "-journal", "-wal", "-shm"] {
        let mut candidate = state.as_os_str().to_os_string();
        candidate.push(suffix);
        if same_file(&resolve(Path::new(&candidate), 0)?, &output) {
            return Err("Controller output conflicts with state or its sidecars");
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn separate_output_with_missing_parents_is_allowed() {
        let dir = tempfile::tempdir().unwrap();
        validate(
            &dir.path().join("state.db"),
            &dir.path().join("reports/new/out.json"),
        )
        .unwrap();
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0);
    }

    #[cfg(unix)]
    #[test]
    fn symlink_parent_traversal_and_loops_fail_safely() {
        use std::os::unix::fs::symlink;
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(dir.path().join("real/nested")).unwrap();
        symlink("real/nested", dir.path().join("alias")).unwrap();
        assert!(validate(&dir.path().join("real/db"), &dir.path().join("alias/../db")).is_err());
        symlink("loop", dir.path().join("loop")).unwrap();
        assert!(validate(&dir.path().join("db"), &dir.path().join("loop")).is_err());
        assert!(!dir.path().join("db").exists());
    }
}
