package vault

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const windowsKeychainPrelude = `Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;
public static class SaberCredential {
 [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)]
 public struct CREDENTIAL {
  public uint Flags; public uint Type; public string TargetName; public string Comment;
  public FILETIME LastWritten; public uint CredentialBlobSize; public IntPtr CredentialBlob;
  public uint Persist; public uint AttributeCount; public IntPtr Attributes;
  public string TargetAlias; public string UserName;
 }
 [DllImport("advapi32.dll", EntryPoint="CredReadW", CharSet=CharSet.Unicode, SetLastError=true)]
 public static extern bool Read(string target,uint type,uint flags,out IntPtr credential);
 [DllImport("advapi32.dll", EntryPoint="CredWriteW", CharSet=CharSet.Unicode, SetLastError=true)]
 public static extern bool Write(ref CREDENTIAL credential,uint flags);
 [DllImport("advapi32.dll")] public static extern void CredFree(IntPtr credential);
}
'@
`

func windowsCommand(ctx context.Context, ref, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", windowsKeychainPrelude+script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "USERPROFILE=" + os.Getenv("USERPROFILE"), "APPDATA=" + os.Getenv("APPDATA"), "SABER_VAULT_REF=" + ref}
	return cmd
}
func keychainRead(ctx context.Context, ref string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "security", "find-generic-password", "-s", "saber-vault", "-a", ref, "-w")
	case "linux":
		cmd = exec.CommandContext(ctx, "secret-tool", "lookup", "application", "saber-vault", "account", ref)
	case "windows":
		cmd = windowsCommand(ctx, ref, `$ptr=[IntPtr]::Zero
if (-not [SaberCredential]::Read(('saber-vault:'+ $env:SABER_VAULT_REF),1,0,[ref]$ptr)) {exit 1}
try {$c=[Runtime.InteropServices.Marshal]::PtrToStructure($ptr,[type][SaberCredential+CREDENTIAL]);$bytes=New-Object byte[] $c.CredentialBlobSize;[Runtime.InteropServices.Marshal]::Copy($c.CredentialBlob,$bytes,0,$bytes.Length);[Console]::Out.Write([Text.Encoding]::UTF8.GetString($bytes))} finally {[SaberCredential]::CredFree($ptr)}
`)
	default:
		return nil, errors.New("OS keychain unavailable on this platform")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("OS keychain unavailable or entry missing; unlock keychain or restore its original entry")
	}
	defer clear(out)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("OS keychain entry is invalid")
	}
	return key, nil
}
func keychainWrite(ctx context.Context, ref string, key []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	encoded := base64.StdEncoding.EncodeToString(key)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "security", "-i")
		cmd.Stdin = strings.NewReader("add-generic-password -U -s saber-vault -a " + ref + " -w '" + encoded + "'\n")
	case "linux":
		cmd = exec.CommandContext(ctx, "secret-tool", "store", "--label=Saber vault", "application", "saber-vault", "account", ref)
		cmd.Stdin = strings.NewReader(encoded)
	case "windows":
		cmd = windowsCommand(ctx, ref, `$bytes=[Text.Encoding]::UTF8.GetBytes([Console]::In.ReadLine());$ptr=[Runtime.InteropServices.Marshal]::AllocHGlobal($bytes.Length)
try {[Runtime.InteropServices.Marshal]::Copy($bytes,0,$ptr,$bytes.Length);$c=New-Object SaberCredential+CREDENTIAL;$c.Type=1;$c.TargetName='saber-vault:'+$env:SABER_VAULT_REF;$c.UserName='Saber';$c.Persist=2;$c.CredentialBlobSize=$bytes.Length;$c.CredentialBlob=$ptr;if (-not [SaberCredential]::Write([ref]$c,0)) {exit 1}} finally {[Runtime.InteropServices.Marshal]::FreeHGlobal($ptr)}
`)
		cmd.Stdin = strings.NewReader(encoded + "\n")
	default:
		return errors.New("OS keychain unavailable on this platform")
	}
	if err := cmd.Run(); err != nil {
		return errors.New("OS keychain write failed; check an unlocked login keychain or Secret Service installation")
	}
	check, err := keychainRead(ctx, ref)
	if err != nil {
		return err
	}
	defer clear(check)
	if !hmac.Equal(check, key) {
		return errors.New("OS keychain write verification failed")
	}
	return nil
}
