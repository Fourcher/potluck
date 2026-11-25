# Potluck

Run one large AI model across the spare computers you already own.

A laptop with 16 GB, an old gaming PC, a Linux box in the closet: none of them can hold a big model alone, but together they can. Potluck pools their memory, splits the model between them, and gives you one chat page and one OpenAI-compatible API on your main computer.

Version 1.11.0. Measured on 17 November 2026: a 36.6 GB, 122-billion-parameter model across a Windows PC and two 16 GB laptops on Wi-Fi, none of which could hold it alone, at 3.3 to 4 words per second (3.2 on 21 November with the newer engine in 1.6.0).

**Recommended model to start with: Inkling 2 Mini** (`AtomicChat/Inkling-2-Mini-Uncensored-GGUF`), starred in the Models tab. Its 8.7 GB IQ2_M size ran on one Windows PC with 30 GB and a 4 GB GTX 1050 Ti at 2.5 to 3 words per second on 21 November 2026. This version has had its refusals removed: it answers what it is asked. It always thinks briefly before answering; switching Think off in the chat page keeps that as short as the model allows.

## Install

- **Mac** (Apple Silicon and Intel, macOS 14 or later): download [Potluck-mac.dmg](https://github.com/Fourcher/potluck/releases/latest/download/Potluck-mac.dmg), open it, drag Potluck to Applications, open it. Signed and notarized by Apple. A welcome window asks what the computer is for and starts a model with one button. Potluck then lives in the menu bar.
- **Windows** (10 and 11): download [Potluck-windows.exe](https://github.com/Fourcher/potluck/releases/latest/download/Potluck-windows.exe) and open it. It installs itself for you (no administrator needed), adds Start menu and desktop shortcuts, and opens its own window, with an icon by the clock. Remove it in Settings > Apps. Not yet signed for Windows, so the first time Windows says "Windows protected your PC": click "More info", then "Run anyway".
- **Linux**: `curl -fsSL https://github.com/Fourcher/potluck/releases/latest/download/install.sh | sh` installs it for your user in `~/.local/bin`, checks it against the release checksums, adds it to the applications menu and starts it.

No account and nothing to pay. Your chats, files and prompts stay on your own computers. Potluck itself only contacts these places, and only to fetch things, never to send what you do:

- **Hugging Face** (huggingface.co): model lists and downloads.
- **GitHub** (github.com, api.github.com, raw.githubusercontent.com): the AI engine (llama.cpp) when a computer first needs it, the daily list of recommended models, and the check for a newer Potluck.
- **Debian's package mirror** (deb.debian.org), on Alpine Linux only: the system libraries the engine needs there.
- **Apple's push service**, only if you turn on phone alerts: the alert text goes from your main computer to your own iPhone.
- **PirateFace** (pirateface.co), only when you search it in the Models tab.
- Any AI service you add yourself under "Your other AI setups" (OpenRouter, say), only for chats you send to it.

Dictation in the chat uses your browser's own speech recognition, which on some browsers runs on the browser maker's servers.

## How you use it

1. **On your main computer**, open Potluck and choose "This is my main computer". Its control panel opens (in its own window, with the Mac and Windows apps).
2. **Add your other computers.** Three ways, none of which needs an account, a password or a long link:
   - *Same network:* on the other computer, open a browser and type the short address the control panel shows (for example `192.168.1.20:7715`). One download, one double-click.
   - *USB stick:* click "Make setup kit", plug the stick into the other computer, double-click "Join".
   - *Already has Potluck:* open it there and choose "spare computer". It finds the main computer by itself; you confirm a 4-digit number on the main computer.
3. **Pick a model** in the Models tab. Every size is marked: runs on one computer, runs pooled across several, or too big for now (and by how much).
4. **Press "Download and run".** Each computer downloads its own share of the model. When it is loaded, "Open chat".
5. **Chat.** The chat page works like the chat apps you know; see "The chat page" below.

Too big for one computer's memory does not always mean you need several. When a model is only somewhat bigger than one computer's memory, the plan also offers **that computer alone, with its disk covering the difference**. See "Pooling or disk?" below.

## Just one computer

Potluck works on a single computer too, with nothing else to set up. On first start choose **Just this computer**. That computer then runs models itself with all the memory it can spare, and:

- **Finds the models you already have.** Under Models, "Already on this computer" lists the GGUF models that LM Studio, Ollama, Hugging Face downloads, llama.cpp, Jan or GPT4All keep, plus `~/models`, your Downloads folder and any folder you add. "Search the whole computer" asks macOS's file index for every model on every drive in about a second (on Windows and Linux it walks the drives, skipping system folders), and Potluck remembers what it found. Each one shows its type, compression, size, how long a conversation it can remember, and whether it fits this computer's memory. "Use it" runs it from where it is: nothing is copied, moved or downloaded again, and "Remove from list" never deletes the other app's file.
- **Uses an AI engine you already have** when it is new enough. A llama.cpp server found on the computer (Homebrew, your own build) is used for models that run here alone if its version is at least Potluck's own; otherwise Potluck installs its own. You can pick one in "On this computer".
- **Lets go of the memory** when the model has not been used for a while (30 minutes by default; choose 10 minutes to 3 hours, or never). The next chat message loads it again by itself. Potluck itself idles at about 13 MB of memory and next to no processor time.
- On a Mac, counts the memory other apps can give up, not only what is idle this second: macOS makes room by compressing them.

Measured on a Mac mini M4 with 16 GB: Inkling 2 Mini (the IQ2_M file, 8.7 GB) loads in about 18 seconds and writes about 18 tokens per second, with an 8,192-token conversation memory.

You can add other computers later; nothing changes for the ones you have.

Each computer has a **pedal**: a slider from parked to flat out that sets how much of its memory and how many of its processor cores it lends, an on/off switch, and optional hours ("only between 22:00 and 07:00"). The main computer coordinates by default and only lends its own muscle if you switch that on.

## The chat page

Open `http://127.0.0.1:7720` on the main computer (or "Open chat" in the control panel). With sharing switched on, other computers on your network can open it at the main computer's address.

- **Conversations** in a sidebar: search, star, rename, export as Markdown, delete. They are kept in your browser on that computer, not on the Potluck computers.
- **Speak or type.** The microphone button takes dictation; say "send message" to send. Replies can be read aloud.
- **Attach files** (their text is included with your message): plain text, PDF, Word, Excel and PowerPoint. Long pasted text becomes an attachment too. **Or attach a folder**. A folder becomes the chat's working folder: the model can list, read and search it, and write files in it.
- **Permission levels** for that folder: chat only, ask every time, read freely (ask before changes), or edit freely. Each request shows what the model wants to do, and for a file it wants to write, the content.
- **Agent mode** (on the computer running Potluck): the model may run commands and scripts and drive apps. "Ask first" shows each command, the folder it runs in and a warning before you say yes; "run freely" skips the question; off is the default. On a Mac it can also run AppleScript to control apps (macOS asks once per app). Commands start in a folder you choose in the control panel, stop after two minutes unless the model asks for longer (at most 15), and stop when you press Stop. Only Potluck's own chat page on this computer can ask for a command: never another computer, even with sharing on, and never another website.
- **Drag and drop** files or a folder anywhere on the page.
- **Models:** see which model is running, switch to another one that is on your computers, or reload with a longer conversation memory. Only the main computer can switch.
- **Usage:** a ring shows how full the conversation's memory is. Click it for tokens read and written, speed, time spent, and which computers are working.
- **Commands:** type `/` in the message box, or press Ctrl/Cmd+K anywhere. `/help` lists what there is.
- **Versions:** trying again or editing a message keeps the earlier answers; flip between them with ‹ ›.
- **Also:** thinking on or off, writing styles, your own standing instructions, stop, a preview panel for HTML and SVG the model writes, light and dark, keyboard shortcuts, and a phone-sized layout.

Two things make it quick on slow hardware. Follow-up messages read only what is new: for Qwen-style models the page sends the conversation back exactly as it was written, where the standard route made the engine read every earlier token again on every turn (measured: 23 new tokens instead of 78). And there are no hidden extra requests, such as asking the model for a title.

## Ask Potluck

Potluck has a helper of its own: a small model (LFM3 1.7B, 1.1 GB) that sets Potluck up and answers questions about it, using Potluck's own controls. Ask "install the best model my computers can run", "help me add my other computers", "put Potluck on my D drive", "connect my iPhone" or "use my LM Studio models too". It asks before anything big (downloading a model, stopping the running model, moving Potluck's folder). On a Mac: Ask Potluck… in the menu bar or the welcome window; on Windows and Linux: the Ask Potluck button in the control panel.

It runs on one of your computers that lends its memory, on the processor only and in an engine of its own, so it answers while a big model downloads or runs; its memory is kept out of the big model's plans. It downloads in the background once Potluck is set up (`"helperOff": true` in the settings file turns it off).

## Models: always current

The Models page starts with **Our picks** (chosen for quality and how well they run at home, each with links to check), then **Trending**, **New this month** and **Most downloaded** on Hugging Face, refreshed every day (`tools/catalog.py`, run daily in this repository; Potluck fetches the newest list once a day). Every number shown is Hugging Face's own; every size is checked against your computers, and once a model has run here Potluck shows the speed it measured.

## Your other AI setups, and other apps

- **Potluck uses your other setups:** under Models, "Your other AI setups" adds LM Studio, Ollama, Jan, a router such as LiteLLM or OpenRouter, or any OpenAI-compatible server (with its key, if it needs one). LM Studio and Ollama running on the same computer are found by themselves. Their models appear in the chat's model menu next to Potluck's own, and in Potluck's `/v1/models`; chats for them go straight to that server.
- **Other apps use Potluck:** under Running, "Let my other devices and apps use it" gives an address and a key. Put them in Apollo, Open WebUI, Msty or any app with a custom OpenAI-compatible server. It answers only on your home-network and Tailscale addresses, and only with the key; "Make a new key" locks out every device using the old one. In a browser on another device, open the address with `?key=…` once.

## With your own AI

Potluck can be the private model your usual AI hands work to. Under **Running**, **Use Potluck from your own AI** has a short note to copy into Claude, ChatGPT, Codex, Cursor or any AI on this computer that can make web requests or run commands. The AI saves the note to its memory, or as a skill. After that, "use Potluck to…" or "run this on the local AI" is enough: it reads Potluck's live instructions from `http://127.0.0.1:7720/potluck/ai` (which model is loaded, how to send it work through the OpenAI-compatible API, and the ground rules), sends the work and says the answer came from Potluck. It asks before switching models, and if Potluck isn't running it asks you to open it rather than using an online model.

## On your iPhone

The Potluck iPhone app (in `ios/`) chats with the model running on your computer, at home over Wi-Fi or anywhere through Tailscale. To pair: in the control panel, under Computers, choose **Pair a phone**, then scan the code with the app (or paste the link under the code). After that the app connects by itself.

- The connection is encrypted with your computer's own certificate, and the app accepts no other: the pairing code carries its fingerprint. Each phone gets a key of its own; **Remove** in the control panel takes it away at once.
- The phone can chat (with thinking on or off, and with text files or PDFs attached), see what is running, and start another model that is already on your computers. It cannot run commands on your computer or change settings: agent mode stays on the computer's own chat page, where each command is shown before it runs.
- A model resting to free memory loads again with the phone's message.
- Your computer only listens for phones while one is paired or a pairing code is showing, and only on its home-network and Tailscale addresses (port 7721). Chats are kept on the phone.

To put it on your iPhone: open `ios/Potluck.xcodeproj` in Xcode, choose your team under Signing & Capabilities, connect the phone and press Run. (If you change `ios/project.yml`, run `xcodegen generate` in `ios/` first.)

The app has a UI test (`ios/PotluckUITests`) that pairs with a running Potluck, asks a question and opens every screen, saving a screenshot of each. Give it a fresh pairing link: `TEST_RUNNER_POTLUCK_LINK='potluck://pair?…' TEST_RUNNER_POTLUCK_SHOTS=/some/folder xcodebuild test -project ios/Potluck.xcodeproj -scheme Potluck -destination 'platform=iOS Simulator,name=iPhone 17'`.

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
- **Chat page** (`chat.go`, `web/chat/`): plain JavaScript, no outside libraries, nothing fetched from the internet. It talks to the model through the OpenAI-compatible API on the same address. Folder access uses the browser's own folder permission, so the model can only reach the folder you picked, and files never pass through Potluck.
- **Your own files:** any `.gguf` file placed in a computer's models folder (shown in the Models tab) can be run from there, for example one fetched with a torrent app. The Models tab can compute its SHA-256 so you can compare it with the one Hugging Face publishes.
- **Graphics cards:** NVIDIA on Windows (CUDA), AMD and Intel cards with their own memory on Windows and Linux, and NVIDIA on Linux (Vulkan), Apple Silicon (Metal). Each engine reports the device it can really use; if it finds none, the model is planned again without it. After loading, Potluck checks the engine's own report against the plan.

Ports on the main computer: 7716 control panel (this computer only), 7717 helpers (encrypted) and discovery, 7715 join page (local network), 7720 chat page and API (this computer only, unless you switch on sharing). Direct links use a UDP port the system picks.

## Measuring your computers, and expected speeds

Each computer card has a **Measure this computer** button. It takes about ten seconds, downloads nothing, and measures how fast the computer reads its memory (which sets how fast a model writes) and how fast the drive holding its models reads random 4 MB blocks, past the computer's cache so it measures the drive and not the memory (from the biggest model file there, or from a 1 GB test file it writes and deletes again). The result is saved as that computer's profile, and marked out of date when its processor, memory or graphics card changes. It is never run on a computer while it lends to a running model. From the command line, `potluck bench` prints the same measurement.

With profiles, the model store shows for each file how fast it is expected to write, for each way it can run: on one computer, pooled, or streamed from disk. Every "Measure speed" result is saved too, and once a model has run here the store shows that measured speed instead of the estimate.

The estimate is checked against every speed measured on the test computers (`bench_test.go`):

| Model and computers | Estimate | Measured |
|---|---|---|
| Qwen3-30B-A3B Q4_K_M, Ryzen 5 2400G reading 32 GB/s, processor only | 5.4–11.8 | 8.0–9.8 |
| Same, with its GTX 1050 Ti holding the part every word uses (Potluck's layout) | 7.2–15.5 | 12.7 |
| Qwen3-4B Q4_K_M, i7-1260P reading 42 GB/s | 6.0–13.1 | 12.6 |
| Qwen3-4B Q2_K, same | 9.0–19.5 | 17.4 |
| Qwen3-4B IQ2_XXS, same | 11.6–25.0 | 13.8–15.9 |
| Qwen3.5-122B IQ2_XXS, pooled over three computers | 2.2–4.7 | 2.3 to 4.1 |
| Qwen3.5-122B IQ2_XXS, streamed from a SATA SSD (443 MB/s) | 0.6–1.4 | 1.0 |

Words per second, writing. The ranges are wide on purpose: they rest on two measurements per computer, not on running the model. A graphics card's help is counted for mixture-of-experts models, where Potluck puts the part every word uses on the card (measured: about 24% less time per word with a 4 GB GTX 1050 Ti). For ordinary (dense) models no measurement backs a factor yet, so their estimates leave the card out and are on the low side where a card does much of the work.

Measured drive speeds (uncached random 4 MB reads): about 3,400 MB/s for an NVMe drive in a laptop, 2,500 MB/s for a MacBook Pro's SSD, 750 MB/s for an external USB SSD, 515 MB/s for a SATA SSD.

## Streaming from disk (InferSSD, experimental)

For the biggest mixture-of-experts models, Potluck can also run a model on one computer with [InferSSD](https://github.com/wici-ai/inferssd), an engine built on llama.cpp that keeps the part of the model used for every word in memory and reads the experts each word needs straight from the SSD. A computer with an empty 2 TB drive can then run a model several times bigger than its memory: InferSSD's authors report 2.5 to 7.3 words a second for a 763-billion-parameter model on fast NVMe drives.

- **When it is offered:** the plan sheet shows "stream from <computer>'s disk" when the model is one InferSSD knows (DeepSeek-V4.1, Muse Spark, Qwen3.5 to 3.8 mixture-of-experts) and a computer can run InferSSD and has room for the file. The model store marks such models "Streams from disk". It is never chosen for you: pooling or plain llama.cpp stay the default.
- **Where it runs:** Windows x64 (a CUDA package for NVIDIA cards from the Turing generation on, otherwise a Vulkan package that also runs on the processor) and Apple Silicon Macs on macOS 15 or later. Not on Linux (InferSSD needs a source build there) or Intel Macs.
- **What Potluck does:** downloads the pinned InferSSD release (0.1.5), checks it against the published SHA-256, keeps it and all its state inside Potluck's data folder, sets its memory budget from the computer's pedal, and serves its chat and API at the usual address.
- **Measured here (18 November 2026):** the 36.6 GB, 122-billion-parameter model on the 30 GB Windows PC (4-core Ryzen 5 2400G, SATA SSD read at 0.55 GB/s). Its 4 GB GTX 1050 Ti is too small for the part of the model every word uses, so Potluck ran InferSSD on the processor alone: about 1 word a second writing and reading (0.95 to 0.98, and 1.0 to 1.06), with the processor at 90% and the drive only partly busy. Plain llama.cpp on the same PC, with the card holding that part, does 2 to 3. So for a model a little bigger than memory, the existing options win; streaming is for models far bigger than memory, on machines with a fast drive and a modern card, which is what InferSSD's own figures come from. Not measured here: the CUDA package, Apple Silicon, NVMe drives.
- **Small graphics cards:** when a computer's card cannot hold the part of the model every word uses, Potluck runs InferSSD without the card instead of letting it fail to load.
- **Limits:** one computer at a time; InferSSD has no network pooling, so the other computers are not used in this mode. Speed depends on the drive: NVMe is what it was measured on; an older SATA SSD will be slower and a hard disk far too slow.

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

    ./build.sh 1.10.0     # needs Go 1.26+; writes dist/potluck-<os>-<arch>, the Windows app, install.sh
    go test ./...

Run `dist/potluck-<your platform>` to start. Keep the other platforms' programs in `dist/` next to it: the USB kit and the join page hand them out. On a Mac the script also builds the Mac app (`macos/`, needs Xcode and xcodegen) into `dist/Potluck.app`, `dist/Potluck-mac.dmg` and the updater's zip, and writes `dist/SHA256SUMS.txt`. Set `POTLUCK_SIGN_ID` to a Developer ID and `POTLUCK_NOTARY_PROFILE` to a stored notarytool profile to sign and notarize.

## Tested so far

- InferSSD streaming end to end on a Windows 10 PC through Potluck: package choice, download and checksum, install, launch with all its state kept in Potluck's data folder, loading without a too-small graphics card, chat through the usual address, and a clean stop.

- Main computer: macOS (Apple Silicon), Windows 10, Alpine Linux. Helpers: Windows 10 (NVIDIA card), macOS 15 (Intel), Alpine Linux (musl).
- All three ways of adding a computer, and all three USB-kit launchers.
- Pooled, single-computer-with-disk, helper graphics card, split-file model, model file already on disk, live switching off and on, sharing with the network.
- The chat page in a Chromium browser against the running cluster: streaming, attachments (text, PDF, Excel; Word and PowerPoint files tested with the same reader outside the browser), answer versions, folder tools with each permission level, model reload with a longer memory, summarize-and-continue, thinking, preview. Dictation and read-aloud are built on the browser's own speech features and have not been tried with a real microphone.
- Direct links between Windows 10, macOS 15 (firewall on, stealth mode on) and Alpine Linux (stateful firewall), with no firewall rule on any of them, including two computers that reach the main computer over different networks.
- The Vulkan engine build, on an NVIDIA card. AMD and Intel cards use the same path but have not been tried on real AMD or Intel hardware.
- The Linux Arm build under emulation (starts, pairs, reports hardware). Windows on Arm is built but untested.
- Lessons built in: GitHub's "latest" llama.cpp release no longer carries binaries; Alpine needs a private glibc; the Intel macOS engine build is unsigned and must be signed locally; the macOS firewall and Local Network privacy rules; a Windows TCP setting that caps single connections (hence many parallel download streams); firewalls that drop discovery replies (hence the address scan fallback); the engine needs helpers named before its device list.

## Known limits

- Inkling 2 Mini cannot be stopped from thinking entirely: with Think off it still writes a short line of reasoning (shown folded under "Thought about it") before its answer.

- The Mac app is signed and notarized. The Windows app is not signed yet, so Windows shows "Windows protected your PC" the first time (More info, Run anyway).
- When a direct link cannot be made, traffic between the running computer and that helper is relayed through the main computer, which is slower.
- With helpers in use, a follow-up message that does not continue exactly where the last answer ended (an edited message, or a model that hides its reasoning from the history) is read again from the start of the conversation.
- The planner knows each helper's connection but not how fast its processor is.
- A model file that is already on disk cannot be fetched by the helpers themselves: the head sends them their share over your network, which is slow the first time.
- PirateFace is list-only: Potluck does not contain a torrent client. Copy the link into your own torrent app, then put the file in a models folder.
- The chat page and API are for this computer only unless you switch on sharing, which needs its key and answers only on home-network and Tailscale addresses.
- Pictures are read only by models that can see (Potluck fetches a model's image add-on with it when there is one). Scanned PDFs and PDFs whose fonts use their own private encoding cannot be read as text; the page says so instead of sending garbage. Older Office formats (.doc, .xls, .ppt) are not read.
- Working in a folder with write access needs a Chromium browser (Chrome, Edge). Safari and Firefox attach a read-only copy.
- Dictation depends on the browser: some recognise speech on the computer, others send audio to the browser maker's service. The page says which. It needs a microphone and only works on the main computer itself or over a secure address.
- Chats live in one browser on one computer. Use export and import in Settings to move them.
- Nothing starts at login unless you switch on "Open Potluck when I log in" in the Mac app's settings (off by default).

## License

MIT. See [LICENSE](LICENSE). Potluck downloads [llama.cpp](https://github.com/ggml-org/llama.cpp) (MIT) as its engine; models you download carry their own licenses.
