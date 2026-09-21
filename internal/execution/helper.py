"""容器内文件操作；输入仅来自 stdin，不把模型参数拼接到 shell 命令中。"""
import json
import os
import stat
import subprocess
import sys

request = json.load(sys.stdin)
tool, args = request["tool"], request["args"]
environment = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp", "LANG": "C.UTF-8"}
environment.update(request["env"] or {})
os.environ.clear()
os.environ.update(environment)


def parent(path):
    if not isinstance(path, str) or not path or path.startswith("/"):
        raise ValueError("path must be relative to /workspace")
    parts = path.split("/")
    if any(p in ("", ".", "..") for p in parts):
        raise ValueError("empty, dot and parent path components are forbidden")
    fd = os.open("/workspace", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd, parts[-1]
    except BaseException:
        os.close(fd)
        raise


def open_file(path, flags):
    directory, name = parent(path)
    try:
        fd = os.open(name, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=directory)
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            os.close(fd)
            raise ValueError("only regular files are supported")
        return fd
    finally:
        os.close(directory)


try:
    if tool == "exec":
        # 容器是隔离边界；shell 的 cwd 本身不是权限边界。
        sys.exit(subprocess.call(["/bin/sh", "-c", args["command"]], env=environment, cwd="/workspace"))
    elif tool == "list_files":
        path = args.get("path", ".")
        if path == ".":
            fd = os.open("/workspace", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        else:
            directory, name = parent(path)
            try:
                fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
            finally:
                os.close(directory)
        try:
            with os.scandir(fd) as entries:
                for i, entry in enumerate(entries):
                    if i >= 10000:
                        raise ValueError("directory exceeds 10000 entries; inspect a subdirectory")
                    print(json.dumps({"name": entry.name, "directory": entry.is_dir(follow_symlinks=False), "symlink": entry.is_symlink()}, ensure_ascii=False))
        finally:
            os.close(fd)
    elif tool == "read_file":
        with os.fdopen(open_file(args["path"], os.O_RDONLY), "rb") as file:
            while block := file.read(65536):
                sys.stdout.buffer.write(block)
    elif tool == "write_file":
        # 先验证目标为普通文件，再截断，避免符号链接或设备文件副作用。
        with os.fdopen(open_file(args["path"], os.O_WRONLY | os.O_CREAT), "wb") as file:
            file.truncate(0)
            file.write(args["content"].encode("utf-8"))
        print("written: " + args["path"])
    elif tool == "apply_patch":
        # 单文件精确替换，拒绝模糊匹配；成功前先校验全部编辑。
        with os.fdopen(open_file(args["path"], os.O_RDWR), "r+", encoding="utf-8", newline="") as file:
            text = file.read(16 * 1024 * 1024 + 1)
            if len(text) > 16 * 1024 * 1024:
                raise ValueError("file exceeds patch size limit")
            for edit in args["edits"]:
                old, new = edit["old"], edit["new"]
                if not old or text.count(old) != 1:
                    raise ValueError("patch old text must match exactly once")
                text = text.replace(old, new, 1)
            file.seek(0)
            file.write(text)
            file.truncate()
        print("patched: " + args["path"])
    else:
        raise ValueError("unknown tool")
except Exception as error:
    print(str(error), file=sys.stderr)
    sys.exit(1)
