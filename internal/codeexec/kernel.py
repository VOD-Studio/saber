"""Saber 原生控制器拥有的 Python 单元进程；不加载任何 Hermes 内核。"""
import ast
import asyncio
import base64
import builtins
import contextlib
import io
import json
import os
import shlex
import sys
import tempfile
import threading
import time
import traceback
import types

protocol_in = sys.stdin
protocol_out = sys.stdout
write_lock = threading.Lock()
authority = {"token": "", "tools": set()}
call_lock = threading.Lock()

def emit(frame):
    frame["token"] = authority["token"]
    with write_lock:
        protocol_out.write(json.dumps(frame, separators=(",", ":")) + "\n")
        protocol_out.flush()

class Output(io.TextIOBase):
    def __init__(self, stream): self.stream = stream
    def write(self, text):
        if text: emit({"type": "output", "stream": self.stream, "data": base64.b64encode(text.encode("utf-8", "replace")).decode()})
        return len(text)
    def flush(self): return None

class ToolError(RuntimeError): pass

def call(name, args=None, **kwargs):
    if name not in authority["tools"]: raise ToolError("tool is unavailable in this code cell: " + name)
    if args is not None and kwargs: raise TypeError("pass a dictionary or keyword arguments, not both")
    arguments = args if args is not None else kwargs
    if not isinstance(arguments, dict): raise TypeError("tool arguments must be an object")
    with call_lock:
        emit({"type": "call", "tool": name, "args": arguments})
        line = protocol_in.readline()
        if not line: raise ToolError("Saber tool RPC disconnected; request is not replayed")
        response = json.loads(line)
        if response.get("type") != "response" or response.get("token") != authority["token"]: raise ToolError("invalid RPC response authority")
        if response.get("error"): raise ToolError(response["error"])
        return response.get("result")

def json_parse(text): return json.loads(text.lstrip("\ufeff"), strict=False)
def retry(fn, max_attempts=3, delay=2):
    for attempt in range(max_attempts):
        try: return fn()
        except Exception:
            if attempt + 1 == max_attempts: raise
            time.sleep(delay * (2 ** attempt))

module = types.ModuleType("saber_tools")
module.call = call
module.json_parse = json_parse
module.shell_quote = shlex.quote
module.retry = retry
module.ToolError = ToolError
sys.modules["saber_tools"] = module
namespace = {"__name__": "__main__", "__builtins__": builtins}
original_cwd = os.getcwd()
strict_dir = None

for line in protocol_in:
    try:
        request = json.loads(line)
        if request.get("type") != "cell": raise ValueError("expected a code cell")
        authority["token"] = request["token"]
        authority["tools"] = set(request["tools"])
        for name in list(vars(module)):
            if name not in {"call", "json_parse", "shell_quote", "retry", "ToolError"} and not name.startswith("__"):
                delattr(module, name)
        for name in authority["tools"]:
            if name.isidentifier():
                setattr(module, name, lambda _name=name, **kwargs: call(_name, kwargs))
        if request.get("mode") == "strict":
            if strict_dir is None: strict_dir = tempfile.mkdtemp(prefix="saber-code-")
            os.chdir(strict_dir)
        else: os.chdir(original_cwd)
        with contextlib.redirect_stdout(Output("stdout")), contextlib.redirect_stderr(Output("stderr")):
            compiled = compile(request["code"], "<saber-code>", "exec", flags=ast.PyCF_ALLOW_TOP_LEVEL_AWAIT)
            value = eval(compiled, namespace, namespace)
            if value is not None: asyncio.run(value)
        emit({"type": "done", "exit_code": 0})
    except BaseException as error:
        if isinstance(error, (KeyboardInterrupt, SystemExit)):
            emit({"type": "done", "exit_code": 1, "error": type(error).__name__ + ": " + str(error)})
        else:
            emit({"type": "output", "stream": "stderr", "data": base64.b64encode(traceback.format_exc().encode()).decode()})
            emit({"type": "done", "exit_code": 1, "error": type(error).__name__ + ": " + str(error)})
    finally:
        authority["tools"] = set()
