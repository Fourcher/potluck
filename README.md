# Potluck

Run one large AI model across the spare computers you already own.

A laptop with 16 GB, an old gaming PC, a Linux box in the closet: none of them can hold a big model alone, but together they can. Potluck pools their memory, splits the model between them, and gives you one chat page and one OpenAI-compatible API on your main computer.

Version 1.1. Measured on 17 November 2026: a 36.6 GB, 122-billion-parameter model across a Windows PC and two 16 GB laptops on Wi-Fi, none of which could hold it alone, at 3.3 to 4 words per second.

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
- **Planner** (`gguf.go`): reads the model's header over the network, takes each computer's offered memory, and decides which layers go where. It emits llama.cpp's `-ngl` and `--tensor-split` values and is tested against llama.cpp's own layer-assignment rule. It keeps as much as it can on the computer that runs the model, brings in as few helpers as will do, and prefers the better-connected ones.
- **Mixture-of-experts models and small graphics cards:** in these models most of each layer is "experts", of which only a few are read per word, while the rest of the layer (the trunk) is read every time. When the running computer's card is too small for whole layers, Potluck puts the trunk of its layers on the card and keeps their experts in memory. A 4 GB card then helps with 32 layers instead of 2.
- **Each computer fetches its own share** (`fetch.go`): the worker caches tensors by hash, so helpers download just their byte ranges of the model file straight from the source instead of receiving them from the head over Wi-Fi.
- **Security** (`identity.go`, `host.go`, `worker.go`): every computer has its own key. Pairing pins the keys (USB kit and join link carry the main computer's fingerprint and a secret; network discovery uses a 4-digit comparison). All traffic between computers is encrypted with those pinned keys. llama.cpp's worker protocol has no authentication, so the engine only ever listens on the computer itself and is reached through that encrypted link.
- **Helpers only connect outwards.** Only the main computer accepts connections (one firewall permission, with a button for it on macOS and Windows).
- **Direct links** (`direct.go`): the computer that runs the model talks to each helper directly instead of through the main computer. The main computer introduces the two over the links it already trusts; both then send each other a few packets at the same moment, which every common firewall treats as a conversation it started, so no firewall rule is needed. The link is QUIC with the same pinned keys. If it cannot be made, the relay through the main computer is used and the control panel says so.
- **Model store** (`store.go`): Hugging Face search, including models that come as several files, and a read-only listing of PirateFace (a community torrent index of Hugging Face models) for when Hugging Face is unreachable.
- **Your own files:** any `.gguf` file placed in a computer's models folder (shown in the Models tab) can be run from there, for example one fetched with a torrent app. The Models tab can compute its SHA-256 so you can compare it with the one Hugging Face publishes.
- **Graphics cards:** NVIDIA on Windows (CUDA), AMD and Intel cards with their own memory on Windows and Linux, and NVIDIA on Linux (Vulkan), Apple Silicon (Metal). Each engine reports the device it can really use; if it finds none, the model is planned again without it. After loading, Potluck checks the engine's own report against the plan.

Ports on the main computer: 7716 control panel (this computer only), 7717 helpers (encrypted) and discovery, 7715 join page (local network), 7720 chat and API (this computer only). Direct links use a UDP port the system picks.

## Pooling or disk?

llama.cpp maps the model file rather than loading it, so a computer can run a model bigger than its memory: the operating system keeps what fits and reads the rest from disk when it is needed. Measured on 16 November 2026 with a 36.6 GB mixture-of-experts model (122B parameters, about 10B active per token):

| Setup | Generation | Prompt |
|---|---|---|
| Version 1.0: pooled, 30 GB PC + two 16 GB laptops, relayed, whole layers | 1.2 to 2.1 words/s | 0.4 to 0.5 words/s |
| Version 1.0: the 30 GB PC alone, model on a SATA SSD | 1.5 to 2.3 | 1.0 to 2.6 |
| Version 1.1: pooled, the PC and one laptop | 3.6 to 4.1 | 8 to 21 |
| Version 1.1: pooled, the PC and both laptops | 3.1 to 3.3 | 8 to 20 |
| Version 1.1: the 30 GB PC alone | 2.0 to 3.0 | 3.3 to 5.4 |

All on Wi-Fi. For a model a few GB over one computer's memory, pooling now wins on this hardware, because the helpers take exactly the part that would otherwise be read from disk for every word. One computer alone remains the simpler choice when the overflow is small or the network is poor, and it is the only choice when no helper is around. The control panel offers both whenever both are possible; "Measure speed" settles it on your hardware.

What made the difference between 1.0 and 1.1, each measured on the cluster above:

- **State saving off when pooled.** For recurrent models the engine saves its working state after every prompt, and with helpers that state travels over the network: 20 seconds per prompt on a good link, and one second conversation stalled for 20 minutes on a poor one. Now off when helpers are in use. The price: a follow-up that cannot continue from the end of the last answer is read again from the start.
- **The card holds the trunk** of the running computer's layers (see above).
- **As few helpers as will do.** One helper was faster than two whenever one was enough (3.6 to 4.1 words/s against 3.1 to 3.3): every helper in the chain adds two network hand-offs per word. When two choices need equally many helpers, the better-connected ones are used.
- **A helper's Wi-Fi sets the floor.** With one laptop's link at its worst, single answers dropped to 1.5 to 2.6 words/s. The control panel flags a helper whose connection is slow.
- **Direct links**: 3.3 words/s against 2.5 to 2.6 relayed through the main computer.
- **Threads.** On a laptop with 4 fast and 8 slow cores, 8 threads beat 12 by 14% writing and 24% reading. On the running computer, every hardware thread helped in the pooled layout (3.8 against 3.3 in a hand-run test) and hurt whenever the disk or whole layers on the processor were involved (1.1 to 1.8 against 2.3 to 2.8), so it is used only in the first case.
- Each of these computers needs about 4 to 14 ms per layer for this model, and the layers run one after another, so more computers add memory, not speed.

## Build

    ./build.sh            # needs Go 1.23+; writes dist/potluck-<os>-<arch>
    go test ./...

Run `dist/potluck-<your platform>` to start. Keep the other platforms' programs in `dist/` next to it: the USB kit and the join page hand them out. On a Mac the script also signs the Mac programs and builds `dist/Potluck.app` (set `POTLUCK_SIGN_ID` to a Developer ID to sign for distribution).

## Tested so far

- Main computer: macOS (Apple Silicon), Windows 10, Alpine Linux. Helpers: Windows 10 (NVIDIA card), macOS 15 (Intel), Alpine Linux (musl).
- All three ways of adding a computer, and all three USB-kit launchers.
- Pooled, single-computer-with-disk, helper graphics card, split-file model, model file already on disk, live switching off and on, sharing with the network.
- Direct links between Windows 10, macOS 15 (firewall on, stealth mode on) and Alpine Linux (stateful firewall), with no firewall rule on any of them, including two computers that reach the main computer over different networks.
- The Vulkan engine build, on an NVIDIA card. AMD and Intel cards use the same path but have not been tried on real AMD or Intel hardware.
- The Linux Arm build under emulation (starts, pairs, reports hardware). Windows on Arm is built but untested.
- Lessons built in: GitHub's "latest" llama.cpp release no longer carries binaries; Alpine needs a private glibc; the Intel macOS engine build is unsigned and must be signed locally; the macOS firewall and Local Network privacy rules; a Windows TCP setting that caps single connections (hence many parallel download streams); firewalls that drop discovery replies (hence the address scan fallback); the engine needs helpers named before its device list.

## Known limits

- Not signed with a paid developer certificate or notarized. Windows shows a SmartScreen warning; on macOS use `Potluck.app` (right-click, Open the first time), the USB kit, or the terminal one-liner on the join page. `build.sh` signs and notarizes when given a Developer ID and stored notary credentials.
- When a direct link cannot be made, traffic between the running computer and that helper is relayed through the main computer, which is slower.
- With helpers in use, a follow-up message that does not continue exactly where the last answer ended (an edited message, or a model that hides its reasoning from the history) is read again from the start of the conversation.
- The planner knows each helper's connection but not how fast its processor is.
- A model file that is already on disk cannot be fetched by the helpers themselves: the head sends them their share over your network, which is slow the first time.
- PirateFace is list-only: Potluck does not contain a torrent client. Copy the link into your own torrent app, then put the file in a models folder.
- The chat page and API are for this computer only unless you switch on sharing with your network. Sharing has no password.
- No start-at-login option, by design.
