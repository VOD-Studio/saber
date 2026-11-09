"""已授权后端的命令 supervisor；stdin/控制文件只接受宿主生成的 NDJSON。"""
import base64
import errno
import fcntl
import json
import os
import pty
import signal
import struct
import subprocess
import sys
import termios
import threading
import time

write_lock = threading.Lock()

def emit(frame):
    with write_lock:
        print(json.dumps(frame, separators=(",", ":")), flush=True)

try:
    request = json.loads(sys.stdin.buffer.readline())
except Exception:
    emit({"type": "error", "message": "invalid supervisor request"})
    sys.exit(2)

if request.get("control"):
    try:
        os.killpg(int(request["pgid"]), int(request["signal"]))
    except ProcessLookupError:
        pass
    except Exception as error:
        emit({"type": "error", "message": str(error)})
        sys.exit(1)
    sys.exit(0)

command = request.get("command")
if not isinstance(command, str) or not command:
    emit({"type": "error", "message": "command is required"})
    sys.exit(2)

environment = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp", "LANG": "C.UTF-8", "TMPDIR": "/tmp"}
environment.update(request.get("env") or {})
use_pty = bool(request.get("pty"))
master = slave = None
try:
    if use_pty:
        master, slave = pty.openpty()
        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", request.get("rows", 24), request.get("cols", 80), 0, 0))
        def session():
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
        process = subprocess.Popen(["/bin/sh", "-c", command], stdin=slave, stdout=slave, stderr=slave,
                                   preexec_fn=session, cwd=request.get("cwd", "/workspace"), env=environment)
        os.close(slave)
    else:
        process = subprocess.Popen(["/bin/sh", "-c", command], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE if request.get("separate_stderr") else subprocess.STDOUT, start_new_session=True,
                                   cwd=request.get("cwd", "/workspace"), env=environment)
except Exception as error:
    if master is not None:
        os.close(master)
    emit({"type": "error", "message": str(error)})
    sys.exit(1)

pgid = os.getpgid(process.pid)
emit({"type": "started", "pgid": pgid})
if request.get("pid_path"):
    start_time = None
    try:
        with open("/proc/" + str(pgid) + "/stat", encoding="ascii") as stat_file:
            start_time = stat_file.read().rsplit(") ", 1)[1].split()[19]
    except (FileNotFoundError, IndexError):
        pass
    with open(request["pid_path"], "w", encoding="ascii") as pid_file:
        json.dump({"pgid": pgid, "start_time": start_time}, pid_file)
        pid_file.flush()
        os.fsync(pid_file.fileno())

def control(frame):
    kind = frame.get("type")
    if kind == "stdin":
        data = base64.b64decode(frame["data"], validate=True)
        if use_pty:
            offset = 0
            while offset < len(data):
                offset += os.write(master, data[offset:])
        else:
            process.stdin.write(data)
            process.stdin.flush()
    elif kind == "eof":
        if use_pty:
            os.write(master, b"\x04")
        else:
            process.stdin.close()
    elif kind == "resize" and use_pty:
        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", int(frame["rows"]), int(frame["cols"]), 0, 0))
        os.killpg(pgid, signal.SIGWINCH)
    elif kind == "signal":
        os.killpg(pgid, int(frame["signal"]))
    else:
        raise ValueError("unsupported process control frame")

def inputs():
    try:
        control_path = request.get("control_path")
        if control_path:
            with open(control_path, "r", encoding="utf-8") as stream:
                while process.poll() is None:
                    line = stream.readline()
                    if not line:
                        time.sleep(0.05)
                        continue
                    control(json.loads(line))
        else:
            for line in sys.stdin.buffer:
                control(json.loads(line))
    except (BrokenPipeError, ProcessLookupError):
        return
    except Exception as error:
        emit({"type": "error", "message": "process control failed: " + str(error)})
        try:
            os.killpg(pgid, signal.SIGTERM)
        except ProcessLookupError:
            pass

threading.Thread(target=inputs, daemon=True).start()

def outputs(source, stream):
    try:
        while True:
            try:
                chunk = os.read(master, 4096) if use_pty else source.read1(4096)
            except OSError as error:
                if use_pty and error.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            emit({"type": "output", "stream": stream, "data": base64.b64encode(chunk).decode()})
    except Exception as error:
        emit({"type": "error", "message": "process output failed: " + str(error)})

output_threads = [threading.Thread(target=outputs, args=(process.stdout, "stdout"), daemon=True)]
if not use_pty and process.stderr is not None:
    output_threads.append(threading.Thread(target=outputs, args=(process.stderr, "stderr"), daemon=True))
for output_thread in output_threads:
    output_thread.start()
code = process.wait()
try:
    os.killpg(pgid, signal.SIGTERM)
    time.sleep(0.05)
    os.killpg(pgid, signal.SIGKILL)
except ProcessLookupError:
    pass
for output_thread in output_threads:
    output_thread.join(timeout=5)
    if output_thread.is_alive():
        emit({"type": "error", "message": "process output did not close after group cleanup"})
if master is not None:
    os.close(master)
emit({"type": "exit", "exit_code": code})
