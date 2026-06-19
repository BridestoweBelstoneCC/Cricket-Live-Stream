# Club Logos Guide
## CricketStream Overlay

Club badges appear as small circular icons next to team names in the scorebar.
They show automatically for any club whose badge you have saved in the `logos/` folder.

---

## How it works

When the PlayCricket API fetches today's match, it retrieves the PlayCricket
club ID for both the home and away teams. The overlay then tries to load:

```
logos/{club_id}.png
```

If the file exists, the badge appears. If not, the space is left blank —
no broken images, no errors.

The opposition's club ID is detected automatically from the day's fixture (along with a
short abbreviation for their name), so you don't have to enter it by hand — just save their
badge as `logos/{their_club_id}.png`.

### Picking a badge manually (new in v2)

If a badge won't match automatically — an unusual club name, a missing ID, or a one-off
opponent — open the control panel and use the **Home badge** / **Away badge** dropdowns to
pick any image in your `logos/` folder for either team. The choice is saved with the match
and overrides the automatic match.

---

## Adding your club badge

1. Find your **PlayCricket club ID** — it's the number in the URL when
   you view your club page on play-cricket.com, or it's the `playcricket_id`
   value in your `config.ini`

2. Save your club badge as:
   ```
   logos/{your_club_id}.png
   ```
   For example, Bridestowe & Belstone CC (club ID 29434) would be:
   ```
   logos/29434.png
   ```

3. That's it — restart the server and your badge will appear on the left
   side of the scorebar next to your team name

---

## Adding opposition badges

Every club you play against has a PlayCricket club ID. When the API fetches
today's match, the away club ID is stored automatically.

To add an opposition badge:

1. Run **Fetch today's match** in the control panel → PlayCricket API card
2. The match details show the opposition club name
3. Find that club's ID on play-cricket.com (number in their page URL)
   — or check `http://127.0.0.1:5000/state` after fetching and look for `away_club_id`
4. Save their badge as `logos/{away_club_id}.png`

Over time your `logos/` folder builds up and you'll have badges for every
club in your league automatically.

---

## Supported file formats

The following formats all work — use whichever gives the best quality:

| Format | Extension | Best for |
|---|---|---|
| PNG | `.png` | Badges with transparency (recommended) |
| SVG | `.svg` | Vector badges (perfect at any size) |
| WebP | `.webp` | Modern format, small file size |
| JPEG | `.jpg` or `.jpeg` | Photographs (no transparency) |
| GIF | `.gif` | Animated badges (rare) |

The overlay checks in that order — PNG first, then SVG, etc. Name the file
with the correct extension for your format.

---

## Where to get club badges

**From play-cricket.com:**
- Go to the club's page on play-cricket.com
- Right-click their badge/logo → Save image
- Save it to your `logos/` folder with the club ID as the filename

**From the club directly:**
- Many clubs have their badge on their website or Facebook page
- A transparent PNG works best in the circular scorebar display

**Creating your own:**
- Any image editor works — Canva, Photoshop, GIMP
- Ideal size: 200×200px or larger (the overlay scales it down to 38px)
- Transparent background looks best against the dark scorebar

---

## Logos folder location

The `logos/` folder sits alongside `server.py`:

```
BBCC Stream/
  server.py
  overlay.html
  config.ini
  logos/          ← add badge files here
    29434.png     ← BBCC badge
    11441.png     ← Okehampton CC (example)
    6075.png      ← Taunton CC (example)
```

Create the folder if it doesn't exist — the server creates it automatically
on first run but it won't break anything if it's missing.

---

## Finding club IDs quickly

After fetching today's match in the control panel, open:
```
http://127.0.0.1:5000/state
```

Look for `away_club_id` in the JSON — that's the away club's PlayCricket ID.
Save their badge as `logos/{that_number}.png` and it appears on the scorebar
immediately (refresh the overlay in OBS: right-click → Refresh).

---

## For non-technical users

You do not need to restart the server to add a new badge. Just:

1. Drop the PNG file into the `logos/` folder
2. Right-click the Overlay source in OBS → **Refresh**
3. The badge appears on the next poll (within 3 seconds)

