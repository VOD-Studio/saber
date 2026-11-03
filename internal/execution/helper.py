"""任务容器内的命令 supervisor；只从 stdin 读取程序构造的 JSON。"""
import base64
import json
import os
import signal
import subprocess
import sys
import threading

try:
    request = json.loads(sys.stdin.readline())
except Exception:
    print(json.dumps({"type": "error", "message": "invalid supervisor request"}))
    sys.exit(2)

if request.get("control"):
    try:
        os.killpg(int(request["pgid"]), int(request["signal"]))
    except Exception as error:
        print(json.dumps({"type": "error", "message": str(error)}))
        sys.exit(1)
    sys.exit(0)

command = request.get("command")
if not isinstance(command, str) or not command:
    print(json.dumps({"type": "error", "message": "command is required"}))
    sys.exit(2)

environment = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp", "LANG": "C.UTF-8", "TMPDIR": "/tmp"}
environment.update(request.get("env") or {})
os.environ.clear()
os.environ.update(environment)

try:
    process = subprocess.Popen(
        ["/bin/sh", "-c", command],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        cwd="/workspace",
    )
except Exception as error:
    print(json.dumps({"type": "error", "message": str(error)}))
    sys.exit(1)

print(json.dumps({"type": "started", "pgid": os.getpgid(process.pid)}), flush=True)


def pump():
    while True:
        chunk = process.stdout.read1(4096)
        if not chunk:
            break
        print(json.dumps({"type": "output", "data": base64.b64encode(chunk).decode()}), flush=True)


thread = threading.Thread(target=pump, daemon=True)
thread.start()
code = process.wait()
thread.join(timeout=5)
print(json.dumps({"type": "exit", "exit_code": code}), flush=True)