"""
OBS Auto-Setup — CricketStream Overlay
────────────────────────────────────────
Connects to OBS via WebSocket and configures everything needed:
  - Creates Main and Replay scenes
  - Creates Overlay browser source in Main
  - Creates ReplayClip media source in Replay
  - Starts the replay buffer
  - Verifies the setup is complete

Run directly:  python obs_setup.py
Or called from quickstart.py automatically.
"""
import json, time, sys, hashlib, base64, os

# Best-effort: a Windows console (or piped stdout) can report a legacy single-byte codepage
# instead of UTF-8, which raises UnicodeEncodeError on this file's checkmarks/arrows.
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

def obs_setup(host="localhost", port=4455, password="", replay_folder="",
              server_port=5000, verbose=True, stream_key="", bitrate_kbps=0):
    """
    Full OBS setup via WebSocket v5.
    Returns (success, list_of_messages).
    """
    try:
        import websocket
    except ImportError:
        return False, ["websocket-client not installed — run: pip install websocket-client"]

    log   = []
    ws_url = f"ws://{host}:{port}"
    msg_id = [0]

    def log_msg(msg, status=""):
        icons = {"ok":"  ✓","warn":"  ⚠","err":"  ✗","":"   "}
        line = f"{icons.get(status,'   ')} {msg}"
        log.append(line)
        if verbose:
            print(line)

    def nid():
        msg_id[0] += 1
        return str(msg_id[0])

    try:
        ws = websocket.WebSocket()
        ws.connect(ws_url, timeout=5)
    except Exception as e:
        log_msg(f"Cannot connect to OBS at {ws_url} — is OBS open?", "err")
        log_msg(f"Error: {e}", "err")
        log_msg("Enable WebSocket: OBS → Tools → WebSocket Server Settings → Enable", "warn")
        return False, log

    def send_msg(op, data=None):
        ws.send(json.dumps({"op": op, "d": data or {}}))

    def wait_op(target_op, timeout=6):
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw: continue
                msg = json.loads(raw)
                if msg.get("op") == target_op:
                    return msg
            except: break
        return None

    def request(req_type, data=None, timeout=6):
        rid = nid()
        payload = {"requestType": req_type, "requestId": rid}
        if data:
            payload["requestData"] = data
        send_msg(6, payload)
        ws.settimeout(timeout)
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                raw = ws.recv()
                if not raw: continue
                msg = json.loads(raw)
                if msg.get("op") == 7 and msg["d"].get("requestId") == rid:
                    return msg["d"]
            except: break
        return None

    # ── Auth ──────────────────────────────────────────────────
    hello = wait_op(0, timeout=5)
    if not hello:
        log_msg("No hello from OBS — WebSocket may not be enabled", "err")
        ws.close(); return False, log

    auth_data = hello["d"].get("authentication")
    if auth_data and password:
        secret = base64.b64encode(
            hashlib.sha256((password + auth_data["salt"]).encode()).digest()
        ).decode()
        auth_response = base64.b64encode(
            hashlib.sha256((secret + auth_data["challenge"]).encode()).digest()
        ).decode()
        send_msg(1, {"rpcVersion": 1, "authentication": auth_response})
    else:
        send_msg(1, {"rpcVersion": 1})

    identified = wait_op(2, timeout=5)
    if not identified:
        log_msg("OBS authentication failed — check your WebSocket password", "err")
        ws.close(); return False, log

    log_msg("Connected to OBS", "ok")

    # ── Get existing scenes ────────────────────────────────────
    scenes_resp = request("GetSceneList")
    existing_scenes = []
    if scenes_resp and scenes_resp.get("requestStatus",{}).get("result"):
        existing_scenes = [s["sceneName"] for s in
                          scenes_resp["responseData"].get("scenes",[])]

    # ── Create scenes ──────────────────────────────────────────
    for scene_name in ["Main", "Replay"]:
        if scene_name in existing_scenes:
            log_msg(f"Scene '{scene_name}' already exists", "ok")
        else:
            r = request("CreateScene", {"sceneName": scene_name})
            if r and r.get("requestStatus",{}).get("result"):
                log_msg(f"Scene '{scene_name}' created", "ok")
            else:
                log_msg(f"Failed to create scene '{scene_name}'", "err")

    # ── Get existing inputs ────────────────────────────────────
    inputs_resp = request("GetInputList")
    existing_inputs = []
    if inputs_resp and inputs_resp.get("requestStatus",{}).get("result"):
        existing_inputs = [i["inputName"] for i in
                          inputs_resp["responseData"].get("inputs",[])]

    # ── Create Overlay browser source in Main ──────────────────
    overlay_url = f"http://localhost:{server_port}/overlay"
    if "Overlay" in existing_inputs:
        # Update settings on existing source
        r = request("SetInputSettings", {
            "inputName": "Overlay",
            "inputSettings": {
                "url":         overlay_url,
                "width":       1920,
                "height":      1080,
                "fps":         24,
                "fps_custom":  True,
                "shutdown":    False,
                "restart_when_active": False,
            }
        })
        log_msg("Overlay browser source settings updated", "ok")
    else:
        r = request("CreateInput", {
            "sceneName":   "Main",
            "inputName":   "Overlay",
            "inputKind":   "browser_source",
            "inputSettings": {
                "url":         overlay_url,
                "width":       1920,
                "height":      1080,
                "fps":         24,
                "fps_custom":  True,
                "shutdown":    False,
                "restart_when_active": False,
                "css":         "",
            }
        })
        if r and r.get("requestStatus",{}).get("result"):
            log_msg("Overlay browser source created in Main", "ok")
        else:
            err = r.get("requestStatus",{}).get("comment","") if r else "no response"
            log_msg(f"Overlay source issue: {err}", "warn")

    # ── Create ReplayClip media source in Replay ───────────────
    if "ReplayClip" in existing_inputs:
        r = request("SetInputSettings", {
            "inputName": "ReplayClip",
            "inputSettings": {
                "restart_on_activate": True,
                "looping":             False,
                "local_file":          "",
            }
        })
        log_msg("ReplayClip media source settings updated", "ok")
    else:
        r = request("CreateInput", {
            "sceneName":   "Replay",
            "inputName":   "ReplayClip",
            "inputKind":   "ffmpeg_source",
            "inputSettings": {
                "restart_on_activate": True,
                "looping":             False,
                "local_file":          "",
                "is_local_file":       True,
            }
        })
        if r and r.get("requestStatus",{}).get("result"):
            log_msg("ReplayClip media source created in Replay", "ok")
        else:
            err = r.get("requestStatus",{}).get("comment","") if r else "no response"
            log_msg(f"ReplayClip source issue: {err}", "warn")

    # ── Point OBS's recording/replay output at the configured folder ──────────
    # config.ini's replay_folder is where the server LOOKS for saved clips; without this,
    # OBS could be SAVING them somewhere else entirely and the replay would silently never
    # find a clip. Simple output mode shares this path between recordings and the replay
    # buffer — that's expected (the config comment describes it as the clips folder).
    if replay_folder:
        try:
            os.makedirs(replay_folder, exist_ok=True)
        except OSError:
            pass
        if os.path.isdir(replay_folder):
            r = request("SetProfileParameter", {"parameterCategory": "SimpleOutput",
                                                "parameterName": "FilePath",
                                                "parameterValue": replay_folder})
            if r and r.get("requestStatus",{}).get("result"):
                log_msg(f"Replay/recording folder set to {replay_folder}", "ok")
            else:
                log_msg("Could not set the replay folder — set it in OBS Settings → Output", "warn")
        else:
            log_msg(f"Replay folder doesn't exist and couldn't be created: {replay_folder}", "warn")

    # ── Apply the YouTube stream key (key-based streaming) ─────────────────────
    # Key-based streaming survives restarts and quality changes; OBS's "connect account"
    # mode with auto-stop ENDS the broadcast whenever the stream stops (learned the hard
    # way in the 2026-07-09 test). Never touched while a stream is actually live.
    if stream_key:
        live = request("GetStreamStatus")
        if live and live.get("responseData", {}).get("outputActive"):
            log_msg("Stream is LIVE — leaving OBS's stream settings alone", "warn")
        else:
            r = request("SetStreamServiceSettings", {
                "streamServiceType": "rtmp_common",
                "streamServiceSettings": {
                    "service": "YouTube - RTMPS",
                    "server":  "rtmps://a.rtmps.youtube.com:443/live2",
                    "key":     stream_key,
                }})
            if r and r.get("requestStatus",{}).get("result"):
                log_msg("YouTube stream key applied — key-based streaming "
                        "(survives restarts and quality changes)", "ok")
            else:
                log_msg("Could not apply the stream key — set it in OBS Settings → Stream", "warn")

    # ── Reset the video bitrate to the configured baseline ─────────────────────
    # The stream-quality ladder (server.py) writes SimpleOutput/VBitrate straight into the
    # OBS profile when it downshifts for congestion, and that value PERSISTS on disk after
    # the server exits — the ladder's own step counter is in-memory only and resets to 0 on
    # restart. Nothing was putting the bitrate back, so a downshifted match handed the next
    # match day a silently-crippled stream from the first ball. Bit twice for real
    # (see diagnostics/STREAM_FREEZE_2026-08-15.md and diagnostics/SEASON_END_2026-09-19.md,
    # both a leftover 875 kbps against a >2700 kbps recommendation). Applying this on every
    # setup run — not just once — is the point: it undoes whatever the last match's ladder
    # left behind, regardless of when that happened. Skipped while a stream is actually live,
    # same precaution as the stream key above, and only meaningful in Simple output mode
    # (Advanced mode ignores SimpleOutput/VBitrate).
    if bitrate_kbps:
        live = request("GetStreamStatus")
        if live and live.get("responseData", {}).get("outputActive"):
            log_msg(f"Stream is LIVE — leaving the bitrate alone (would reset to "
                    f"{bitrate_kbps} kbps)", "warn")
        else:
            mode_resp = request("GetProfileParameter", {"parameterCategory": "Output",
                                                         "parameterName": "Mode"})
            mode = (mode_resp or {}).get("responseData", {}).get("parameterValue") or "Simple"
            if mode != "Simple":
                log_msg("OBS is in Advanced output mode — bitrate reset only applies to "
                        "Simple output, skipped", "warn")
            else:
                r = request("SetProfileParameter", {"parameterCategory": "SimpleOutput",
                                                    "parameterName": "VBitrate",
                                                    "parameterValue": str(bitrate_kbps)})
                if r and r.get("requestStatus", {}).get("result"):
                    log_msg(f"Video bitrate reset to {bitrate_kbps} kbps (undoes any "
                            f"leftover downshift from a previous match)", "ok")
                else:
                    log_msg(f"Could not reset the bitrate — check it manually in OBS "
                            f"Settings → Output (should be {bitrate_kbps} kbps)", "warn")

    # ── Enable OBS's Dynamic Bitrate (congestion handled without disconnects) ──
    # Off by default in OBS and buried in Settings → Advanced → Network. With it on, the
    # encoder bitrate flexes automatically when the connection struggles — the seamless
    # first line of defence for grounds with poor internet. Applies from the next stream
    # start. (The server's stream sentinel is the second line — see /stream/monitor.)
    r = request("SetProfileParameter", {"parameterCategory": "Output",
                                        "parameterName": "DynamicBitrate",
                                        "parameterValue": "true"})
    if r and r.get("requestStatus",{}).get("result"):
        log_msg("Dynamic bitrate enabled — congestion managed without disconnects", "ok")
    else:
        log_msg("Could not enable dynamic bitrate — turn it on in OBS Settings → Advanced → Network", "warn")

    # ── Switch to Main scene ───────────────────────────────────
    request("SetCurrentProgramScene", {"sceneName": "Main"})
    log_msg("Active scene set to Main", "ok")

    # ── Enable AND start the replay buffer ─────────────────────
    # This used to only START it, which fails outright when the buffer isn't ENABLED in
    # OBS's output settings — the default for a fresh OBS install. The operator got a
    # warning mid-setup telling them to go and tick a box themselves, and if they missed
    # it (a volunteer, on a match morning) nothing announced the problem again: the first
    # anyone knew was a wicket falling and no replay appearing. Replays are half the point
    # of the product, so setup now turns the buffer on rather than asking.
    #
    # There's no WebSocket request to enable it, but the profile parameter behind the
    # checkbox is settable: Simple mode keeps it at SimpleOutput/RecRB (+RecRBTime for the
    # length in seconds), Advanced at AdvOut/RecRB. Set whichever matches the current mode.
    mode_resp = request("GetProfileParameter", {"parameterCategory": "Output",
                                                "parameterName": "Mode"})
    out_mode = (mode_resp or {}).get("responseData", {}).get("parameterValue") or "Simple"

    # Set it in BOTH sections, not just the active one. OBS keeps a separate RecRB under
    # [SimpleOutput] and [AdvOut], and only honours the one matching the current output
    # mode — so enabling just the active one leaves a trap: switch OBS to Advanced later
    # (to pick an encoder, say) and replays silently stop working, with nothing to
    # indicate why. Verified against a real profile's basic.ini, which showed RecRB=true
    # in one section and RecRB=false in the other after setting only the active one.
    enabled_ok = False
    for category in ("SimpleOutput", "AdvOut"):
        r = request("SetProfileParameter", {"parameterCategory": category,
                                            "parameterName": "RecRB",
                                            "parameterValue": "true"})
        ok = bool(r and r.get("requestStatus", {}).get("result"))
        if ok:
            # Long enough to cover the run-up and the shot, matching the setup guides' 25s.
            request("SetProfileParameter", {"parameterCategory": category,
                                            "parameterName": "RecRBTime",
                                            "parameterValue": "25"})
        # Only the section OBS is actually using decides whether this worked.
        if category == ("AdvOut" if out_mode != "Simple" else "SimpleOutput"):
            enabled_ok = ok

    if enabled_ok:
        log_msg(f"Replay buffer enabled, 25s ({out_mode} output mode — and pre-set for the "
                f"other mode, so switching later won't turn replays off)", "ok")
    else:
        log_msg("Could not enable the replay buffer automatically — tick it in OBS "
                "Settings → Output → Replay Buffer", "warn")

    rb_status = request("GetReplayBufferStatus")
    rb_active = False
    if rb_status and rb_status.get("requestStatus",{}).get("result"):
        rb_active = rb_status["responseData"].get("outputActive", False)

    if rb_active:
        log_msg("Replay buffer already running", "ok")
    else:
        r = request("StartReplayBuffer")
        if r and r.get("requestStatus",{}).get("result"):
            log_msg("Replay buffer started", "ok")
        else:
            err = r.get("requestStatus",{}).get("comment","") if r else "no response"
            if "not active" in str(err).lower() or "already" in str(err).lower():
                log_msg("Replay buffer started", "ok")
            elif enabled_ok:
                # We just ticked the box, so OBS knows about it — but an output that was
                # disabled when OBS started may only be creatable after a restart. Say so
                # specifically rather than repeating "go and enable it", which is now done.
                log_msg("Replay buffer enabled but wouldn't start yet — restart OBS once "
                        "and re-run setup; it starts automatically from then on", "warn")
            else:
                log_msg(f"Replay buffer: {err or 'enable in OBS Settings → Output → Replay Buffer'}", "warn")

    ws.close()
    return True, log


if __name__ == "__main__":
    import configparser, os

    cfg = configparser.ConfigParser()
    cfg_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.ini")
    if os.path.exists(cfg_path):
        cfg.read(cfg_path)

    password      = cfg.get("OBS", "obs_password",   fallback="")
    replay_folder = cfg.get("OBS", "replay_folder",  fallback="")
    replay_folder = os.path.expanduser(replay_folder)
    stream_key    = cfg.get("Stream", "youtube_stream_key", fallback="").strip()
    try:
        bitrate_kbps = int(cfg.get("Stream", "bitrate_kbps", fallback="").strip() or 0)
    except ValueError:
        bitrate_kbps = 0

    print()
    print("OBS Auto-Setup — CricketStream Overlay")
    print("────────────────────────────────────────")
    ok, messages = obs_setup(
        password      = password,
        replay_folder = replay_folder,
        stream_key    = stream_key,
        bitrate_kbps  = bitrate_kbps,
    )
    print()
    if ok:
        print("  OBS is configured and ready.")
    else:
        print("  Setup incomplete — see messages above.")
    print()
