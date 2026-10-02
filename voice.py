"""Spoken commentary: text to a WAV file with the computer's own voices. Stdlib only.

Free and offline — Windows' built-in speech engine (preferring a British English voice
such as Microsoft Hazel), macOS `say` (Daniel), or espeak on Linux. The text always goes
through a file, never a command line, so a commentary line can't break or inject into the
command. Optional and off by default in the panel: these voices are serviceable, not
broadcast quality.
"""
import os
import shutil
import subprocess
import sys
import tempfile

_PS_SCRIPT = r'''
param([string]$TextFile, [string]$OutFile, [string]$Culture)
Add-Type -AssemblyName System.Speech
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer
$v = $s.GetInstalledVoices() | Where-Object { $_.Enabled -and $_.VoiceInfo.Culture.Name -eq $Culture } | Select-Object -First 1
if ($v) { $s.SelectVoice($v.VoiceInfo.Name) }
$s.Rate = 0
$s.SetOutputToWaveFile($OutFile)
$s.Speak([System.IO.File]::ReadAllText($TextFile, [System.Text.Encoding]::UTF8))
$s.Dispose()
'''


def synthesize(text, out_path, culture="en-GB", timeout=30):
    """Write `text` as speech to out_path (WAV). Returns (ok, message). Never raises."""
    text = (text or "").strip()
    if not text:
        return False, "nothing to say"
    work = tempfile.mkdtemp(prefix="voice_")
    try:
        txt = os.path.join(work, "line.txt")
        with open(txt, "w", encoding="utf-8") as f:
            f.write(text)
        os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
        if sys.platform == "win32":
            ps1 = os.path.join(work, "speak.ps1")
            with open(ps1, "w", encoding="utf-8") as f:
                f.write(_PS_SCRIPT)
            cmd = ["powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
                   "-File", ps1, "-TextFile", txt, "-OutFile", out_path, "-Culture", culture]
        elif sys.platform == "darwin":
            cmd = ["say", "-v", "Daniel", "-o", out_path, "--data-format=LEI16@22050", "-f", txt]
        else:
            engine = shutil.which("espeak-ng") or shutil.which("espeak")
            if not engine:
                return False, "no speech engine (install espeak-ng)"
            cmd = [engine, "-v", "en-gb", "-w", out_path, "-f", txt]
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        if r.returncode != 0 and sys.platform == "darwin":
            # Daniel not installed: the default voice is better than silence
            cmd = ["say", "-o", out_path, "--data-format=LEI16@22050", "-f", txt]
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        if r.returncode != 0 or not os.path.exists(out_path) or os.path.getsize(out_path) < 100:
            return False, ((r.stderr or "").strip().splitlines() or ["speech failed"])[-1][:200]
        return True, out_path
    except subprocess.TimeoutExpired:
        return False, "speech engine took too long"
    except Exception as e:
        return False, str(e)[:200]
    finally:
        shutil.rmtree(work, ignore_errors=True)
