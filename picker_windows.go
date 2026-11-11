//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// The modern Windows "Select Folder" dialog (IFileOpenDialog with FOS_PICKFOLDERS),
// driven from PowerShell so the app needs no extra libraries. Falls back to the
// classic folder browser if the modern dialog can't be created.
const pickFolderPS = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$init = $env:SECV_PICK_INIT
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
$owner.ShowInTaskbar = $false
$owner.FormBorderStyle = 'None'
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Opacity = 0
$owner.Show()
$owner.Activate()
$result = $null
try {
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class SecvFolderPicker {
  [ComImport, Guid("DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7")] private class FileOpenDialogRCW {}
  [ComImport, Guid("42f85136-db7e-439c-85f1-e4075d135fc8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  private interface IFileDialog {
    [PreserveSig] int Show(IntPtr parent);
    void SetFileTypes(uint cFileTypes, IntPtr rgFilterSpec);
    void SetFileTypeIndex(uint iFileType);
    void GetFileTypeIndex(out uint piFileType);
    void Advise(IntPtr pfde, out uint pdwCookie);
    void Unadvise(uint dwCookie);
    void SetOptions(uint fos);
    void GetOptions(out uint pfos);
    void SetDefaultFolder(IShellItem psi);
    void SetFolder(IShellItem psi);
    void GetFolder(out IShellItem ppsi);
    void GetCurrentSelection(out IShellItem ppsi);
    void SetFileName([MarshalAs(UnmanagedType.LPWStr)] string pszName);
    void GetFileName([MarshalAs(UnmanagedType.LPWStr)] out string pszName);
    void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string pszTitle);
    void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string pszText);
    void SetFileNameLabel([MarshalAs(UnmanagedType.LPWStr)] string pszLabel);
    void GetResult(out IShellItem ppsi);
    void AddPlace(IShellItem psi, int fdap);
    void SetDefaultExtension([MarshalAs(UnmanagedType.LPWStr)] string pszDefaultExtension);
    void Close(int hr);
    void SetClientGuid(ref Guid guid);
    void ClearClientData();
    void SetFilter(IntPtr pFilter);
  }
  [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  private interface IShellItem {
    void BindToHandler(IntPtr pbc, ref Guid bhid, ref Guid riid, out IntPtr ppv);
    void GetParent(out IShellItem ppsi);
    void GetDisplayName(uint sigdnName, [MarshalAs(UnmanagedType.LPWStr)] out string ppszName);
    void GetAttributes(uint sfgaoMask, out uint psfgaoAttribs);
    void Compare(IShellItem psi, uint hint, out int piOrder);
  }
  [DllImport("shell32.dll", CharSet = CharSet.Unicode, PreserveSig = false)]
  private static extern void SHCreateItemFromParsingName(string pszPath, IntPtr pbc, [MarshalAs(UnmanagedType.LPStruct)] Guid riid, out IShellItem ppv);
  public static string Pick(string initial, IntPtr owner) {
    IFileDialog dlg = (IFileDialog)new FileOpenDialogRCW();
    uint opts;
    dlg.GetOptions(out opts);
    dlg.SetOptions(opts | 0x20u | 0x40u | 0x800u);
    dlg.SetTitle("Choose where to save SEC filing PDFs");
    dlg.SetOkButtonLabel("Select Folder");
    if (!String.IsNullOrEmpty(initial) && System.IO.Directory.Exists(initial)) {
      try {
        IShellItem start;
        SHCreateItemFromParsingName(initial, IntPtr.Zero, typeof(IShellItem).GUID, out start);
        dlg.SetFolder(start);
      } catch {}
    }
    int hr = dlg.Show(owner);
    if (hr != 0) return null;
    IShellItem res;
    dlg.GetResult(out res);
    string path;
    res.GetDisplayName(0x80058000u, out path);
    return path;
  }
}
"@
  $result = [SecvFolderPicker]::Pick($init, $owner.Handle)
} catch {
  $fb = New-Object System.Windows.Forms.FolderBrowserDialog
  $fb.Description = 'Choose where to save SEC filing PDFs'
  $fb.ShowNewFolderButton = $true
  if ($init -and (Test-Path -LiteralPath $init)) { $fb.SelectedPath = $init }
  if ($fb.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { $result = $fb.SelectedPath }
}
$owner.Close()
if ($result) { Write-Output ('PICKED:' + $result) } else { Write-Output 'CANCELED' }
`

func pickFolder(initial string) (string, bool, error) {
	u := utf16.Encode([]rune(pickFolderPS))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.Env = append(os.Environ(), "SECV_PICK_INIT="+initial)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimPrefix(string(out), "\xef\xbb\xbf")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PICKED:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "PICKED:")), true, nil
		}
		if line == "CANCELED" {
			return "", false, nil
		}
	}
	logf("folder picker failed: %v %s", err, text)
	return "", false, errors.New("couldn't open the folder picker. You can type or paste the folder path instead")
}

// The standard Windows Open and Save dialogs for session files, also from PowerShell.
const pickFilePS = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
$owner.ShowInTaskbar = $false
$owner.FormBorderStyle = 'None'
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Opacity = 0
$owner.Show()
$owner.Activate()
if ($env:SECQ_PICK_SAVE -eq '1') {
  $dlg = New-Object System.Windows.Forms.SaveFileDialog
  $dlg.OverwritePrompt = $true
  $dlg.AddExtension = $true
  $dlg.DefaultExt = $env:SECQ_PICK_EXT
  $dlg.FileName = $env:SECQ_PICK_NAME
} else {
  $dlg = New-Object System.Windows.Forms.OpenFileDialog
  $dlg.CheckFileExists = $true
}
$dlg.Title = $env:SECQ_PICK_TITLE
$dlg.Filter = $env:SECQ_PICK_LABEL + ' (*.' + $env:SECQ_PICK_EXT + ')|*.' + $env:SECQ_PICK_EXT + '|All files (*.*)|*.*'
if ($env:SECQ_PICK_DIR -and (Test-Path -LiteralPath $env:SECQ_PICK_DIR)) { $dlg.InitialDirectory = $env:SECQ_PICK_DIR }
$result = $null
if ($dlg.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { $result = $dlg.FileName }
$owner.Close()
if ($result) { Write-Output ('PICKED:' + $result) } else { Write-Output 'CANCELED' }
`

// pickFile shows an Open dialog, or a Save dialog when save is set, for files with
// the extension ext (".secq"), described in the dialog as label.
func pickFile(save bool, title, dir, name, ext, label string) (string, bool, error) {
	u := utf16.Encode([]rune(pickFilePS))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	mode := "0"
	if save {
		mode = "1"
	}
	cmd.Env = append(os.Environ(), "SECQ_PICK_SAVE="+mode, "SECQ_PICK_TITLE="+title, "SECQ_PICK_DIR="+dir, "SECQ_PICK_NAME="+name,
		"SECQ_PICK_EXT="+strings.TrimPrefix(ext, "."), "SECQ_PICK_LABEL="+label)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimPrefix(string(out), "\xef\xbb\xbf")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PICKED:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "PICKED:")), true, nil
		}
		if line == "CANCELED" {
			return "", false, nil
		}
	}
	logf("file picker failed: %v %s", err, text)
	return "", false, errors.New("couldn't open the file window")
}
