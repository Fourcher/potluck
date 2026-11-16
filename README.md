# Potluck

Run one large AI model across the spare computers you already own.

A laptop with 16 GB, an old gaming PC, a Linux box in the closet: none of them can hold a big model alone, but together they can. Potluck pools their memory, splits the model between them, and gives you one chat page and one OpenAI-compatible API on your main computer.

Working title, version 0.1. First real run (16 November 2026): a 36.6 GB, 122-billion-parameter model across a Windows PC and two 16 GB laptops, none of which could hold it alone.

## How you use it

1. **On your main computer**, open Potluck and choose "This is my main computer". A control panel opens in your browser.
2. **Add your other computers.** Three ways, none of which needs an account, a password or a long link:
   - *Same network:* on the other computer, open a browser and type the short address the control panel shows (for example `192.168.1.20:7715`). One download, one double-click.
   - *USB stick:* click "Make setup kit", plug the stick into the other computer, double-click "Join".
   - *Already has Potluck:* open it there and choose "spare computer". It finds the main computer by itself; you confirm a 4-digit number on the main computer.
3. **Pick a model** in the Models tab. Every size is marked: runs on one computer, runs pooled across several, or too big for now (and by how much).
4. **Press "Download and run".** Each computer downloads its own share of the model. When it is loaded, "Open chat".

Too big for one computer's memory does not always mean you need several. When a model is only somewhat bigger than one computer's memory, the plan also offers **that computer alone, with its disk covering the difference**. See "Pooling or disk?" below.

Each computer has a **pedal**: a slider from parked to flat out that sets how much of its memory and how many of its processor cores it lends, an on/off switch, and optional hours ("only between 22:00 and 07:00"). The main computer coordinates by default and only lends its own muscle if you switch that on.

## What is inside

One self-contained program per platform (about 9 MB, no installer). Written in Go; the control panel is a local web page.

- **Engine:** [llama.cpp](https://github.com/ggml-org/llama.cpp) and its RPC backend. Potluck downloads the matching build for each computer (checked against the SHA-256 GitHub publishes) and pins one version everywhere, because the worker protocol is version-checked.
- **Planner** (`gguf.go`): reads the model's header over the network, takes each computer's offered memory, and decides which layers go where. It emits llama.cpp's `-ngl` and `--tensor-split` values and is tested against llama.cpp's own layer-assignment rule.
- **Each computer fetches its own share** (`fetch.go`): the worker caches tensors by hash, so helpers download just their byte ranges of the model file straight from the source instead of receiving them from the head over Wi-Fi.
- **Security** (`identity.go`, `host.go`, `worker.go`): every computer has its own key. Pairing pins the keys (USB kit and join link carry the main computer's fingerprint and a secret; network discovery uses a 4-digit comparison). All traffic between computers is TLS with those pinned keys. llama.cpp's worker protocol has no authentication, so the engine only ever listens on the computer itself and is reached through that encrypted link.
- **Helpers only connect outwards.** Only the main computer accepts connections (one firewall permission, with a button for it on macOS and Windows).
- **Model store** (`store.go`): Hugging Face search, and a read-only listing of PirateFace (a community torrent index of Hugging Face models) for when Hugging Face is unreachable.

Ports on the main computer: 7716 control panel (this computer only), 7717 helpers (encrypted) and discovery, 7715 join page (local network), 7720 chat and API (this computer only).

## Pooling or disk?

llama.cpp maps the model file rather than loading it, so a computer can run a model bigger than its memory: the operating system keeps what fits and reads the rest from disk when it is needed. Measured on 16 November 2026 with a 36.6 GB mixture-of-experts model (122B parameters, about 10B active per token):

| Setup | Generation | Prompt |
|---|---|---|
| Pooled: 30 GB PC + two 16 GB laptops, all on Wi-Fi | 1.2 to 2.1 tokens/s | 0.4 to 0.5 tokens/s |
| The 30 GB PC alone, model on a SATA SSD | 1.5 to 2.3 tokens/s | 1.0 to 2.6 tokens/s |

So for a model a few GB over one computer's memory, that computer alone is as fast or faster, because nothing crosses the network. Pooling is for the cases the disk cannot carry: models well beyond any one computer's memory (every word would wait on gigabytes of disk reads), dense models that touch every weight for every word, and computers with hard disks. Wired network between the computers shifts the balance back toward pooling. The control panel offers both whenever both are possible; "Measure speed" settles it on your hardware.

## Build

    ./build.sh            # needs Go 1.23+; writes dist/potluck-<os>-<arch>
    go test ./...

Run `dist/potluck-<your platform>` to start. Keep the other platforms' programs in `dist/` next to it: the USB kit and the join page hand them out. On a Mac the script also signs the Mac programs and builds `dist/Potluck.app` (set `POTLUCK_SIGN_ID` to a Developer ID to sign for distribution).

## Tested so far

- Main computer: macOS (Apple Silicon). Helpers: Windows 10 (NVIDIA GPU), macOS 15 (Intel), Alpine Linux (musl).
- All three ways of adding a computer.
- Lessons already built in: GitHub's "latest" llama.cpp release no longer carries binaries; Alpine needs a private glibc; the Intel macOS engine build is unsigned and must be signed locally; the macOS firewall and Local Network privacy rules; a Windows TCP setting that caps single connections (hence many parallel download streams); firewalls that drop discovery replies (hence the address scan fallback).

## Known limits and next steps

- Not signed with a paid developer certificate or notarized. Windows shows a SmartScreen warning; on macOS use `Potluck.app` (right-click, Open the first time), the USB kit, or the terminal one-liner on the join page.
- A helper's graphics card is used on Windows PCs with NVIDIA cards (card first, then memory). AMD and Intel cards, and NVIDIA on Linux, are not used yet; Apple Silicon is.
- Tensor traffic between head and helpers is relayed through the main computer. A direct path between them is planned.
- When a computer's hours end, it is switched off, or it drops out, the model is laid out again on what is left, or waits and returns by itself. A pedal moved under a running model applies when you press "Apply now".
- The chat page and API are for this computer only unless you switch on sharing with your network. Sharing has no password.
- Models split across several files are not supported yet. PirateFace is list-only (copy the torrent link).
- Disk use stays bounded: each helper keeps only its share of the current model, old engine versions are removed, and the Models tab lists every downloaded file with a delete button.
- No start-at-login option, by design for now.
