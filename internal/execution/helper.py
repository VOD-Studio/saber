"""容器内命令执行；输入仅来自 stdin，不把模型参数拼接到宿主 shell。"""
import json
import os
import subprocess
import sys

request = json.load(sys.stdin)
args = request["args"]
environment = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp", "LANG": "C.UTF-8"}
environment.update(request["env"] or {})
os.environ.clear()
os.environ.update(environment)

if request["tool"] != "exec":
    print("unsupported tool", file=sys.stderr)
    sys.exit(1)
sys.exit(subprocess.call(["/bin/sh", "-c", args["command"]], env=environment, cwd="/workspace"))