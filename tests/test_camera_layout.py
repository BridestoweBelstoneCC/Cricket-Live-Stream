"""Camera size and stacking in OBS scenes (server._arrange_camera_item, obs_setup.py).

OBS drops a newly added source at its native size in the top-left corner, ON TOP of
everything already in the scene. Verified on OBS 32.1.1 before this existed: a 720p camera
filled a quarter of the 1920x1080 canvas, covered the Overlay in Main (no scorebar) and
covered ReplayClip in Replay (a replay showed the live camera). The bowler-end scene was
created with cameras only, so cutting to it hid the scorebar too.

FakeScenes follows OBS's own rules: index 0 is the bottom, CreateSceneItem adds on top,
SetSceneItemIndex moves an item to that position. The layouts asserted here were also
checked against the real OBS, including screenshots of which camera each scene shows.
"""
import os
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO)

import server  # noqa: E402

CANVAS = (1920, 1080)
DEFAULT_TR = {"boundsType": "OBS_BOUNDS_NONE", "positionX": 0, "positionY": 0,
              "scaleX": 1, "scaleY": 1}


def kind_of(source):
    """Cameras and the replay clip are video sources; the Overlay is a browser source;
    anything called Logo is an image (a sponsor's, say)."""
    if source == "Overlay":
        return "browser_source"
    if source.startswith("Logo"):
        return "image_source"
    return "ffmpeg_source"


class FakeScenes:
    def __init__(self, scenes):
        # scene -> list of [source, id, transform], bottom first
        self.scenes = {s: [[src, n, dict(DEFAULT_TR)] for n, src in enumerate(items, 1)]
                       for s, items in scenes.items()}
        self.next_id = 100

    def order(self, scene):
        """Top first, like OBS's sources list."""
        return [i[0] for i in reversed(self.scenes[scene])]

    def tr(self, scene, src):
        return next(i[2] for i in self.scenes[scene] if i[0] == src)

    def __call__(self, rt, rd=None):
        rd = rd or {}
        sc = self.scenes.get(rd.get("sceneName"))
        find = lambda: next(i for i in sc if i[1] == rd["sceneItemId"])
        if rt == "GetSceneItemList":
            return {"sceneItems": [{"sourceName": s, "sceneItemId": n, "sceneItemIndex": k,
                                    "inputKind": kind_of(s)}
                                   for k, (s, n, _) in enumerate(sc)]}
        if rt == "CreateSceneItem":
            self.next_id += 1
            sc.append([rd["sourceName"], self.next_id, dict(DEFAULT_TR)])
            return {}
        if rt == "GetSceneItemTransform":
            return {"sceneItemTransform": dict(find()[2])}
        if rt == "SetSceneItemTransform":
            find()[2].update(rd["sceneItemTransform"])
            return {}
        if rt == "SetSceneItemIndex":
            item = find()
            sc.remove(item)
            sc.insert(rd["sceneItemIndex"], item)
            return {}
        raise AssertionError(f"unexpected request {rt}")


def arrange(obs, scene, cam, home, fresh, overlay=True):
    server._arrange_camera_item(obs, lambda r: r, scene, cam, CANVAS,
                                home=home, fresh=fresh, overlay_exists=overlay)


class TestCameraLayout(unittest.TestCase):
    def test_new_camera_goes_under_the_graphics_in_main(self):
        obs = FakeScenes({"Main": ["Overlay", "Cam"]})          # OBS put Cam on top
        arrange(obs, "Main", "Cam", home=True, fresh=True)
        self.assertEqual(obs.order("Main"), ["Overlay", "Cam"])

    def test_new_camera_goes_under_the_replay_clip(self):
        obs = FakeScenes({"Replay": ["ReplayClip", "Cam"]})
        arrange(obs, "Replay", "Cam", home=False, fresh=True)
        self.assertEqual(obs.order("Replay"), ["ReplayClip", "Cam"])

    def test_new_camera_fills_the_canvas(self):
        obs = FakeScenes({"Main": ["Overlay", "Cam"]})
        arrange(obs, "Main", "Cam", home=True, fresh=True)
        tr = obs.tr("Main", "Cam")
        self.assertEqual(tr["boundsType"], "OBS_BOUNDS_SCALE_INNER")
        self.assertEqual((tr["boundsWidth"], tr["boundsHeight"]), CANVAS)

    def test_untouched_existing_camera_is_fitted(self):
        # Added by the old code: still at native size in the corner.
        obs = FakeScenes({"Main": ["Cam", "Overlay"]})
        arrange(obs, "Main", "Cam", home=True, fresh=False)
        self.assertEqual(obs.tr("Main", "Cam")["boundsType"], "OBS_BOUNDS_SCALE_INNER")

    def test_hand_placed_camera_is_left_where_it_is(self):
        # A deliberate picture-in-picture, say. Not ours to undo.
        obs = FakeScenes({"Main": ["Cam", "Overlay"]})
        obs.tr("Main", "Cam").update({"positionX": 1400, "positionY": 50,
                                      "scaleX": 0.25, "scaleY": 0.25})
        arrange(obs, "Main", "Cam", home=True, fresh=False)
        tr = obs.tr("Main", "Cam")
        self.assertEqual((tr["positionX"], tr["scaleX"]), (1400, 0.25))
        self.assertEqual(tr["boundsType"], "OBS_BOUNDS_NONE")

    def test_existing_camera_covering_the_graphics_is_moved_under_them(self):
        obs = FakeScenes({"Main": ["Overlay", "Cam"]})
        arrange(obs, "Main", "Cam", home=True, fresh=False)
        self.assertEqual(obs.order("Main"), ["Overlay", "Cam"])

    def test_correct_existing_stack_is_not_touched(self):
        obs = FakeScenes({"Main": ["Other", "Cam", "Overlay"]})
        arrange(obs, "Main", "Cam", home=True, fresh=False)
        self.assertEqual(obs.order("Main"), ["Overlay", "Cam", "Other"])

    def test_sponsor_logo_above_the_scorebar_stays_there(self):
        # The layout on the maintainer's real install: logos above the Overlay on purpose.
        # Adding a camera must slot it under the Overlay, not lift the Overlay over them.
        obs = FakeScenes({"Main": ["Display", "Overlay", "Logo1", "Logo2", "Cam"]})
        arrange(obs, "Main", "Cam", home=True, fresh=True)
        self.assertEqual(obs.order("Main"), ["Logo2", "Logo1", "Overlay", "Cam", "Display"])

    def test_bowler_scene_gets_the_overlay(self):
        # Created with cameras only, so the over-boundary auto-cut hid the scorebar.
        obs = FakeScenes({"Main-Bowler": ["Cam2"]})
        arrange(obs, "Main-Bowler", "Cam2", home=True, fresh=True)
        self.assertEqual(obs.order("Main-Bowler"), ["Overlay", "Cam2"])

    def test_no_overlay_source_yet_adds_nothing(self):
        obs = FakeScenes({"Main-Bowler": ["Cam2"]})
        arrange(obs, "Main-Bowler", "Cam2", home=True, fresh=True, overlay=False)
        self.assertEqual(obs.order("Main-Bowler"), ["Cam2"])

    def test_two_cameras_each_scene_shows_its_own(self):
        # The real-OBS sequence: camera 2 added (home Main-Bowler, also in Main), then
        # camera 1 re-added (home Main, also in Main-Bowler). Matches what OBS showed.
        obs = FakeScenes({"Main": ["Cam1", "Overlay"], "Main-Bowler": [],
                          "Replay": ["Cam1", "ReplayClip"]})
        for scene, home in (("Main-Bowler", True), ("Replay", False), ("Main", False)):
            obs.scenes[scene].append(["Cam2", 50 + len(scene), dict(DEFAULT_TR)])
            arrange(obs, scene, "Cam2", home=home, fresh=True)
        obs.scenes["Main-Bowler"].append(["Cam1", 90, dict(DEFAULT_TR)])
        arrange(obs, "Main-Bowler", "Cam1", home=False, fresh=True)
        for scene, home in (("Main", True), ("Replay", False)):
            arrange(obs, scene, "Cam1", home=home, fresh=False)
        self.assertEqual(obs.order("Main"), ["Overlay", "Cam1", "Cam2"])
        self.assertEqual(obs.order("Main-Bowler"), ["Overlay", "Cam2", "Cam1"])
        self.assertEqual(obs.order("Replay")[0], "ReplayClip")


class TestCoveringPictures(unittest.TestCase):
    """obs_prep.covering_pictures + the move obs_setup.py makes every match day, run on
    FakeScenes: each covering camera is moved DOWN to just beneath the graphic."""

    def repair(self, names, source="Overlay"):
        import obs_prep
        obs = FakeScenes({"S": names})
        for _ in range(len(names)):
            items = obs("GetSceneItemList", {"sceneName": "S"})["sceneItems"]
            over = obs_prep.covering_pictures(items, source)
            if not over:
                break
            mine = next(i for i in items if i["sourceName"] == source)["sceneItemIndex"]
            obs("SetSceneItemIndex", {"sceneName": "S", "sceneItemId": over[0]["sceneItemId"],
                                      "sceneItemIndex": mine})
        return obs.order("S")

    def test_camera_covering_the_overlay(self):
        self.assertEqual(self.repair(["Overlay", "Cam"]), ["Overlay", "Cam"])

    def test_camera_dragged_over_everything_leaves_the_logos_alone(self):
        # The real-OBS case: lifting the Overlay instead put it above the logo too.
        self.assertEqual(self.repair(["Display", "Overlay", "Logo1", "Cam"]),
                         ["Logo1", "Overlay", "Cam", "Display"])

    def test_already_right_is_untouched(self):
        self.assertEqual(self.repair(["Display", "Cam", "Overlay", "Logo1", "Logo2"]),
                         ["Logo2", "Logo1", "Overlay", "Cam", "Display"])

    def test_two_cameras_over_the_replay_clip(self):
        self.assertEqual(self.repair(["ReplayClip", "Cam1", "Logo1", "Cam2"], "ReplayClip"),
                         ["Logo1", "ReplayClip", "Cam2", "Cam1"])

    def test_screen_capture_counts_as_a_picture(self):
        import obs_prep
        self.assertTrue(obs_prep.is_picture_kind("monitor_capture"))
        self.assertTrue(obs_prep.is_picture_kind("av_capture_input_v2"))
        self.assertFalse(obs_prep.is_picture_kind("image_source"))
        self.assertFalse(obs_prep.is_picture_kind("text_gdiplus_v3"))


class TestObsSetupRepairsTheStack(unittest.TestCase):
    """obs_setup.py talks to a real WebSocket, so these are source checks; the behaviour
    was confirmed on OBS 32.1.1 (a camera put back on top was moved under the Overlay)."""

    def setUp(self):
        with open(os.path.join(REPO, "obs_setup.py"), encoding="utf-8") as f:
            self.src = f.read()

    def test_graphics_put_back_above_the_camera_every_run(self):
        self.assertIn('(("Main", "Overlay"), ("Replay", "ReplayClip"))', self.src)
        self.assertIn("covering_pictures(lst, source)", self.src)
        # Never "to the top of the scene": that buried a club's sponsor logos.
        self.assertNotIn('"sceneItemIndex": top}', self.src)

    def test_replay_clip_fitted_to_canvas(self):
        # Replays are recorded at the OUTPUT resolution (720p by default).
        block = self.src[self.src.index('if source == "ReplayClip" and untouched_placement'):]
        self.assertIn("fill_canvas_transform(", block[:600])


if __name__ == "__main__":
    unittest.main()
