# emojiv

Picks emoji for a shortcode-style query (`:crying-face`, `:movie-about-dinosaurs`) using
[Jev](https://docs.typesafe.ai) (TypeSafe's `/v1/systemone` API).

![Test UI: results for "family vacation" with the debug panel](demo.png)

```sh
export TYPESAFE_API_KEY=...
go run .    # http://localhost:8080
```

## How it works

The query is normalized (`:movie-about-dinosaurs` → `movie about dinosaurs`) and sent in
the `state` (together with shared context) of a single Jev request. Emoji are grouped into
99 buckets by Unicode subgroup (subgroups over Jev's 255-option Choice limit are split), and
each bucket gets two questions, all evaluated in parallel:

- `related:<id>` — Noul: would an emoji from this category fit a message about the text?
- `bucket:<id>` — Choice: which emoji in this category is most associated with the text?

Each emoji is scored `P(related) × P(emoji | bucket)`. The Noul answers are independent, so
several categories can score high at once. (A single Choice over categories put 100% on one
category, which hid every other association.) Results are cached in memory per query.

Endpoints: `GET /api/search?q=&n=&raw=1`, `GET /api/request?q=` (shows the Jev request body).
Emoji data: `data/emoji-test.txt` from unicode.org (fully-qualified, skin tones excluded).
To update it to the latest Unicode release, run `go generate`, which downloads it again.
