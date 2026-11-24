build.ps1 (and tools/e2e/calistir.ps1) copy the freshly built HukukFixDM-host.exe here before building the
app, which embeds it (install_windows.go) so the release is a single HukukFixDM.exe. The .exe is not committed.
