# Windows Remotes over SSH

`quil --remote <host>` also works against a Windows PC, using the OpenSSH
Server that ships in Windows 10 and Windows 11. This page covers the
Windows-specific setup; see [Remote daemon over SSH](features.md#remote-daemon-over-ssh)
and the [Remote Daemon Attach PRD](roadmap/remote-daemon.md) for the feature
itself.

## 1. Install OpenSSH Server

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service sshd -StartupType Automatic   # optional: start sshd at boot
```

## 2. Open the firewall

Installing the capability normally adds the inbound rule for you. Check it:

```powershell
Get-NetFirewallRule -Name OpenSSH-Server-In-TCP
```

If it's missing, create it:

```powershell
New-NetFirewallRule -Name OpenSSH-Server-In-TCP -DisplayName 'OpenSSH Server (sshd)' `
  -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22
```

## 3. Authorize your key

**Admin accounts** (a member of `Administrators`) use a machine-wide file
with a locked-down ACL — sshd refuses it if any broader group can read or
write it:

```
C:\ProgramData\ssh\administrators_authorized_keys
```

Paste your public key into that file, then lock it down:

```powershell
icacls.exe "C:\ProgramData\ssh\administrators_authorized_keys" /inheritance:r /grant "Administrators:F" /grant "SYSTEM:F"
```

**Standard (non-admin) accounts** use the ordinary per-user file instead, with
no special ACL needed:

```
%USERPROFILE%\.ssh\authorized_keys
```

## 4. The default shell

Quil quotes its remote commands for whichever shell OpenSSH runs — `quil
remote setup` detects it and records the answer, so you don't set anything
in quil itself.

- **cmd.exe** (OpenSSH's own default) is tested end to end, including a
  profile path with spaces, `(x86)`, and `@`.
- **PowerShell** as the ssh `DefaultShell` is supported by the code and
  covered by unit tests, but has not yet been exercised against a real
  Windows host — treat it as unverified until it has been.
- **bash**, from Git for Windows, also works, and is treated exactly like a
  POSIX remote.

To change the default shell:

```powershell
New-ItemProperty -Path "HKLM:\SOFTWARE\OpenSSH" -Name DefaultShell `
  -Value "C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe" -PropertyType String -Force
```

(Point `-Value` at `pwsh.exe`'s own path to use PowerShell 7 instead.) Remove
the value to go back to `cmd.exe`:

```powershell
Remove-ItemProperty -Path "HKLM:\SOFTWARE\OpenSSH" -Name DefaultShell
```

## 5. Tailscale

If the two machines aren't already on the same network, install
[Tailscale](https://tailscale.com/) on both and attach by the machine's
Tailscale name:

```bash
quil --remote user@<tailscale-name>
```

No port forwarding and no public IP needed — Tailscale gives the two
machines a private address that reaches each other wherever they are.

## 6. Install quil on the host

```bash
quil remote setup user@host
```

This installs to (or upgrades in place under):

```
%LOCALAPPDATA%\Programs\quil
```

Windows PowerShell started by OpenSSH cannot read data piped over ssh's own
stdin, so the install is three separate ssh round trips rather than one:
**prepare** (create the install directory), **copy** (extract the archive
with the host's own `C:\Windows\System32\tar.exe`, which has no such
limitation — needs Windows 10 version 1803 / Server 2019 or later), and
**verify-and-swap** (check every file's SHA-256, rename a running `quil.exe`
aside to `quil.exe.old`, then move the new files in). Setup then registers
the logon task for you (§8), so the daemon can start in your desktop session
afterward.

If quil is already installed elsewhere on the host, setup adopts that copy
in place — but only when its folder is writable by you and by no broad group
(`Everyone`, `Users`, `Authenticated Users`, `INTERACTIVE`). When the
existing folder is shared that widely, setup installs its own separate copy
under `%LOCALAPPDATA%\Programs\quil` instead of touching it.

## 7. Adding quil to PATH (optional)

`quil remote setup` does **not** change PATH. Quil records the exact install
path and uses it for every future `quil --remote` attach, so PATH isn't
needed for the daemon to start. Add the install directory to PATH yourself
only if you also want to run `ssh host quil ...` by hand:

```powershell
setx PATH "$env:PATH;$env:LOCALAPPDATA\Programs\quil"
```

(`setx` only affects new sessions — open a fresh one afterward.)

## 8. The logon task

```bash
quil daemon install-logon          # register
quil daemon install-logon --remove # remove
```

`quil remote setup` runs this for you, but you can also run it by hand,
including from an ssh session, with no elevation required. It registers a
per-user "run at logon" scheduled task (`Quil daemon`) so the daemon starts
in your desktop session — with your saved logins and the ability to open
windows — the next time you log on, instead of running detached and
`[limited]` (§9).

Registering and removing both run under a lowered, non-admin token, even
from an elevated or admin ssh session — so a task registered over ssh can
still be removed later from an ordinary desktop shell. A task registered by
an **older** quil build, or by some other means from an elevated session,
may be owned by `Administrators` instead; if `install-logon --remove`
reports access denied, remove it over ssh or from an elevated terminal
instead.

## 9. What `[limited]` means

`[limited]` in the status bar means the daemon is running in session 0 —
started over ssh with no logon task registered yet (or before you've logged
on since registering one). It still works, with three differences:

- **Saved logins aren't available.** Credentials in Windows Credential
  Manager (`git`, `gh`, and anything else that reads it) aren't visible to a
  session-0 process, so its panes can't use them.
- **Windows a pane opens are invisible.** A browser window for a login flow,
  or an editor a pane launches, has no desktop to appear on.
- **Toasts still work.** Desktop notifications are raised by the TUI on your
  own machine, not by the remote daemon, so they're unaffected.

An admin account gets a full admin token over ssh; quil never starts the
daemon with it. A session-0 daemon always runs at Medium integrity with
`Administrators` disabled — never elevated — whether or not a logon task is
installed. `[limited]` marks the narrower case where it additionally has no
desktop to run in.

**To leave `[limited]`:** log on at the PC (console or RDP) and run `quil
daemon restart` there — the daemon detects the desktop session and starts
fully. Or install the logon task once (§8): the next time you log on, the
daemon starts in your desktop session automatically.
