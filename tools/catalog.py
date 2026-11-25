#!/usr/bin/env python3
"""Builds catalog/models.json: the models Potluck shows before anyone searches.

Run daily (see .github/workflows/catalog.yml). It keeps the hand-picked list in
catalog/picks.json and adds, from Hugging Face's public API:
  - trending: GGUF models people are downloading right now,
  - new: GGUF models released in the last 30 days that people are trying now,
  - popular: the most downloaded GGUF models of all time.
Every number comes straight from Hugging Face; nothing is scored or ranked by Potluck here.
Standard library only.
"""
import datetime as dt
import json
import os
import sys
import urllib.parse
import urllib.request

API = "https://huggingface.co/api/models"
HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

# Chat and vision-chat models only (image generators, speech and embeddings are left out).
TEXT = {"text-generation", "image-text-to-text", "any-to-any", None, ""}
SKIP_WORDS = ("embed", "rerank", "whisper", "tts", "-image-", "image-2", "diffusion", "vae", "lora", "draft", "mtp-only")


def fetch(params):
    q = urllib.parse.urlencode({"filter": "gguf", "direction": "-1", "full": "false", **params})
    req = urllib.request.Request(f"{API}?{q}", headers={"User-Agent": "potluck-catalog"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


def keep(m):
    rid = (m.get("id") or "").lower()
    if m.get("pipeline_tag") not in TEXT:
        return False
    return not any(w in rid for w in SKIP_WORDS)


def entry(m):
    return {
        "repo": m["id"],
        "downloads": m.get("downloads", 0),
        "likes": m.get("likes", 0),
        "created": (m.get("createdAt") or "")[:10],
    }


def main():
    picks = json.load(open(os.path.join(ROOT, "catalog", "picks.json")))["picks"]
    lists = json.load(open(os.path.join(ROOT, "catalog", "lists.json")))["lists"]
    taken = {p["repo"] for p in picks}

    def take(models, n):
        out = []
        for m in models:
            if len(out) >= n:
                break
            if keep(m) and m["id"] not in taken:
                taken.add(m["id"])
                out.append(entry(m))
        return out

    hot = fetch({"sort": "trendingScore", "limit": "300"})
    trending = take(hot, 12)
    # New: released in the last 30 days and among what people are trying now.
    month_ago = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=30)).isoformat()
    recent = [m for m in hot if (m.get("createdAt") or "") >= month_ago and m.get("likes", 0) >= 15]
    recent.sort(key=lambda m: m.get("likes", 0), reverse=True)
    new = take(recent, 12)
    popular = take(fetch({"sort": "downloads", "limit": "80"}), 12)

    cat = {
        "version": 1,
        "updated": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%MZ"),
        "source": "Hugging Face public API (downloads, likes and trending as Hugging Face reports them)",
        "picks": picks,
        "trending": trending,
        "new": new,
        "popular": popular,
        "lists": lists,
    }
    path = os.path.join(ROOT, "catalog", "models.json")
    old = None
    if os.path.exists(path):
        try:
            old = json.load(open(path))
        except ValueError:
            pass
    # Only write when a list changed, so the daily job doesn't commit just a new timestamp.
    if old and all(old.get(k) == cat[k] for k in ("picks", "trending", "new", "popular", "lists")):
        print("catalog unchanged")
        return 0
    with open(path, "w") as f:
        json.dump(cat, f, indent=1)
        f.write("\n")
    print(f"catalog: {len(picks)} picks, {len(trending)} trending, {len(new)} new, {len(popular)} popular")
    return 0


if __name__ == "__main__":
    sys.exit(main())
