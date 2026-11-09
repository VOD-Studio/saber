package execution

const sshRecoverySource = `import glob,json,os,signal,sys,time,shutil
root=sys.argv[1]
if not os.path.isdir(root): sys.exit(0)
for directory in glob.glob(root+"/p-*"):
 pidfile=directory+"/pid"
 if not os.path.isfile(pidfile): continue
 with open(pidfile) as stream: record=json.load(stream)
 pgid=int(record["pgid"])
 try: os.killpg(pgid,0)
 except ProcessLookupError: continue
 statfile="/proc/"+str(pgid)+"/stat"
 if not record.get("start_time") or not os.path.isfile(statfile): raise RuntimeError("cannot prove identity of orphaned SSH process; inspect remote task before clearing quarantine")
 with open(statfile) as stream: current=stream.read().rsplit(") ",1)[1].split()[19]
 if current!=record["start_time"]: raise RuntimeError("SSH process group ID was reused; refusing to signal unrelated process")
 os.killpg(pgid,signal.SIGTERM)
 time.sleep(1)
 try:
  with open(statfile) as stream: current=stream.read().rsplit(") ",1)[1].split()[19]
  if current==record["start_time"]: os.killpg(pgid,signal.SIGKILL)
 except FileNotFoundError: pass
shutil.rmtree(root)
`
