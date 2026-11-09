"""官方云 SDK 传输桥。授权、任务、命令、同步与恢复策略全部由 Go 控制。"""
import base64
import contextlib
import io
import json
import math
import os
import shlex
import sys
import tempfile
from datetime import timedelta

os.environ.setdefault("VERCEL_TELEMETRY_DISABLED", "1")
provider = sandbox = client = None

def execute(command, data=None):
    if provider == "modal":
        proc = sandbox.exec("/bin/sh", "-c", command)
        if data is not None:
            for offset in range(0, len(data), 1024 * 1024):
                proc.stdin.write(data[offset:offset + 1024 * 1024])
                proc.stdin.drain()
        proc.stdin.write_eof()
        proc.stdin.drain()
        # 两路并行读取，避免 stderr 填满管道造成死锁。
        import concurrent.futures
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            out = pool.submit(proc.stdout.read)
            err = pool.submit(proc.stderr.read)
            code = proc.wait()
            output = out.result() + err.result()
        return output.encode() if isinstance(output, str) else output, code
    staged = None
    if data is not None:
        import secrets
        staged = "/tmp/.saber-stdin-" + secrets.token_hex(16)
        put(staged, data)
        command = "exec 3<" + shlex.quote(staged) + "; rm -f " + shlex.quote(staged) + "; " + command + " <&3"
    if provider == "daytona":
        result = sandbox.process.exec("/bin/sh -c " + shlex.quote(command), timeout=30)
        return (result.result or "").encode(), result.exit_code
    result = sandbox.run_command("sh", ["-c", command])
    output = result.output()
    return output if isinstance(output, bytes) else str(output or "").encode(), result.exit_code

def put(path, data):
    if provider == "modal":
        proc = sandbox.exec("/bin/sh", "-c", "umask 077; base64 -d > " + shlex.quote(path))
        payload = base64.b64encode(data)
        for offset in range(0, len(payload), 1024 * 1024):
            proc.stdin.write(payload[offset:offset + 1024 * 1024]); proc.stdin.drain()
        proc.stdin.write_eof(); proc.stdin.drain()
        if proc.wait() != 0:
            raise RuntimeError("Modal upload failed")
    elif provider == "daytona":
        sandbox.fs.upload_file(data, path)
        sandbox.fs.set_file_permissions(path, mode="600")
    else:
        sandbox.write_files([{"path": path, "content": data, "mode": 0o600}])

def get(path):
    if provider == "modal":
        output, code = execute("base64 " + shlex.quote(path))
        if code != 0:
            raise RuntimeError("Modal download failed")
        return base64.b64decode(output)
    with tempfile.TemporaryDirectory(prefix="saber-cloud-download-") as directory:
        dest = os.path.join(directory, "data")
        if provider == "daytona":
            sandbox.fs.download_file(path, dest)
        else:
            sandbox.download_file(path, dest)
        with open(dest, "rb") as stream:
            return stream.read()

def handle(request):
    global provider, sandbox, client
    op = request["op"]
    if op == "open":
        provider = request["provider"]
        resource = request.get("resource")
        if provider == "modal":
            import modal
            if resource:
                sandbox = modal.Sandbox.from_id(resource)
            else:
                app = modal.App.lookup("saber-execution", create_if_missing=True)
                image = modal.Image.from_registry(request["image"])
                sandbox = modal.Sandbox.create("sleep", "infinity", image=image, app=app,
                    cpu=request["cpu"], memory=request["memory_mb"], timeout=request["lifetime_seconds"],
                    block_network=not request["network"])
            return {"resource": sandbox.object_id}
        if provider == "daytona":
            from daytona import Daytona, CreateSandboxFromImageParams, Resources
            client = Daytona()
            if resource:
                sandbox = client.get(resource)
            else:
                sandbox = client.create(CreateSandboxFromImageParams(image=request["image"],
                    name=request["name"], labels={"saber_executor": request["owner"]}, auto_stop_interval=0,
                    resources=Resources(cpu=request["cpu"], memory=max(1, math.ceil(request["memory_mb"]/1024)),
                                        disk=max(1, math.ceil(request["disk_mb"]/1024)))))
            return {"resource": sandbox.id}
        if provider == "vercel":
            from vercel.sandbox import Sandbox, Resources
            if resource:
                sandbox = Sandbox.get(resource)
            else:
                kwargs = {"timeout": timedelta(seconds=request["lifetime_seconds"]),
                          "resources": Resources(vcpus=request["cpu"], memory=request["memory_mb"])}
                if request.get("runtime"):
                    kwargs["runtime"] = request["runtime"]
                elif request.get("vercel_image"):
                    kwargs["image"] = request["vercel_image"]
                sandbox = Sandbox.create(**kwargs)
            resource = getattr(sandbox, "sandbox_id", None) or sandbox.sandbox.id
            return {"resource": resource}
        raise ValueError("unsupported cloud provider")
    if sandbox is None:
        raise RuntimeError("cloud sandbox is not open")
    if op == "exec":
        data = base64.b64decode(request["stdin"]) if "stdin" in request else None
        output, code = execute(request["command"], data)
        return {"output": base64.b64encode(output).decode(), "exit_code": code}
    if op == "put":
        put(request["path"], base64.b64decode(request["data"], validate=True))
        return {}
    if op == "get":
        return {"data": base64.b64encode(get(request["path"])).decode()}
    if op == "close":
        if provider == "modal": sandbox.terminate()
        elif provider == "daytona": client.delete(sandbox)
        else: sandbox.stop(blocking=True, timeout=timedelta(seconds=15))
        sandbox = None
        return {}
    raise ValueError("unsupported cloud bridge operation")

for line in sys.stdin:
    try:
        request = json.loads(line)
        response = {"result": handle(request)}
        if request.get("op") == "open" and request.get("state_file"):
            record = request["record"]
            record["resource"] = response["result"]["resource"]
            state_file = request["state_file"]
            fd, temporary = tempfile.mkstemp(prefix=".cloud-resource-", dir=os.path.dirname(state_file))
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as stream:
                    json.dump(record, stream)
                    stream.flush()
                    os.fsync(stream.fileno())
                os.replace(temporary, state_file)
            finally:
                if os.path.exists(temporary): os.unlink(temporary)
    except Exception as error:
        response = {"error": type(error).__name__ + ": " + str(error)}
    print(json.dumps(response, separators=(",", ":")), flush=True)
