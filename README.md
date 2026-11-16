# Tokenizer

A standalone desktop tokenizer in the spirit of [OpenAI's tokenizer](https://platform.openai.com/tokenizer). It runs the real byte-pair-encoding (BPE) vocabularies used by OpenAI models, with a Go backend and a small vanilla JavaScript frontend.

On Windows, `Tokenizer.exe` opens its own native window — no browser, address bar, or localhost URL to manage. The same code can also run as a local web server if you prefer.

**No LLM, GPU, API key, or network connection is required at runtime.** Tokenization is a deterministic text-processing step, not something that needs model weights.

![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/desktop-Windows%2010%2F11-0078D4?logo=windows)
![No dependencies](https://img.shields.io/badge/frontend-vanilla%20JS-f7df1e)

## Features

- Native desktop window on Windows (WebView2 host, no browser chrome)
- Optional local web-server mode
- Four public tiktoken encodings:
  - `o200k_base` — GPT-4o family / newer models (200k vocabulary)
  - `cl100k_base` — GPT-4 / GPT-3.5 era (100k vocabulary)
  - `p50k_base` — text-davinci-002/003 and Codex (50k vocabulary)
  - `r50k_base` — original GPT-2 / GPT-3 (50k vocabulary)
- Live token count, character count, and byte count
- Colored token breakdown with visible whitespace
- Hover details (token ID, byte length, escaped text) and click-to-copy IDs
- Incomplete UTF-8 fragments displayed as `\xNN` instead of mojibake
- Recognition of each encoding's special tokens (e.g. `<|endoftext|>`)
- Embedded web assets and vocabularies: one self-contained executable
- No third-party frontend libraries, no CDN, no telemetry

## Desktop app

Requirements: Windows 10 or 11 with the **Microsoft Edge WebView2 Runtime**. It is preinstalled on Windows 11 and current Windows 10; if it is missing, the app tells you and links to the installer.

1. Double-click **`Tokenizer.exe`**.
2. Type text, or click **Show example**.
3. Close the window to quit. The internal server shuts down with it.

The executable is unsigned because it is built locally. If Windows SmartScreen appears, choose **More info → Run anyway**.

Desktop details:

- The UI is served to the native window over a private, random `127.0.0.1` port that is only reachable from this PC and is closed when the window closes.
- A second launch focuses the existing window instead of starting another copy.
- External links open in your default browser.
- Logs are written to `%LOCALAPPDATA%\Tokenizer\tokenizer.log`.
- WebView2 cache lives in `%LOCALAPPDATA%\Tokenizer\WebView2`.

To rebuild the desktop app and the server:

```powershell
.\build.ps1
```

Or build manually:

```powershell
go build -trimpath -tags desktopapp -ldflags "-H windowsgui -s -w" -o Tokenizer.exe .
go build -trimpath -ldflags "-s -w" -o TokenizerServer.exe .
```

The desktop build uses [go-webview2](https://github.com/jchv/go-webview2), which is pure Go and does not require cgo or a C compiler. The window icon and per-monitor DPI manifest are compiled from `rsrc_windows_amd64.syso`; regenerate it after changing `assets/icon.ico` or `app.manifest`:

```powershell
go run github.com/akavel/rsrc@latest -manifest app.manifest -ico assets/icon.ico -o rsrc_windows_amd64.syso
```

### Changing the app icon

1. Replace `assets/icon.ico` with a multi-size Windows icon (16, 32, 48, 64, 128, and 256 px work well).
2. Regenerate the resource file with the command above.
3. Run `.\build.ps1` and relaunch `Tokenizer.exe`.

`rsrc` numbers the manifest as resource ID 1 and the first icon group as ID 2 when both flags are used. `desktop_windows.go` loads ID 2 (`IconId: 2`). If you generate the resources with different flags or tools, make the icon group ID and `IconId` match, otherwise Windows displays its generic application icon.

## Browser/server mode

`TokenizerServer.exe` runs the same UI as a local website:

```powershell
.\TokenizerServer.exe
```

Then open <http://127.0.0.1:8080>.

For development, `go run .` does the same thing. The desktop executable can also be forced into server mode with `.\Tokenizer.exe -serve`, though the server binary is more convenient because it has a console.

The server binds to `127.0.0.1:8080` by default. To use another address or expose it on your LAN:

```powershell
.\TokenizerServer.exe -addr :8080
```

> The server mode has no authentication. Keep the default loopback binding unless you intentionally want other devices to reach it.

## Editing the UI

The interface is plain HTML, CSS, and JavaScript:

| File | What it controls |
|---|---|
| `web/index.html` | Page structure, headings, buttons, labels, footer |
| `web/styles.css` | Theme colors, spacing, layout, token colors, responsive rules |
| `web/app.js` | API calls, token rendering, counts, tabs, copy/toast behavior |
| `web/favicon.svg` | Icon shown in browser mode |

Useful starting points:

- **Theme colors and fonts:** CSS custom properties at the top of `web/styles.css` (`--bg`, `--accent`, `--text`, `--font`, and so on). Token chip palettes are the `.token.c0` … `.token.c11` rules.
- **Rendered token limit:** `MAX_RENDERED_TOKENS` near the top of `web/app.js`.
- **Example text:** `EXAMPLE_TEXT` near the top of `web/app.js`.
- **Element lookups:** the `els` object near the top of `web/app.js`. If you add an element in `index.html`, add its selector there.
- **Window title and size:** `desktop_windows.go`.
- **Encoding names and labels:** `internal/tokenizer/definitions.go`.

### Fast iteration (`-dev`)

Normally the UI is embedded in the executable, so edits require a rebuild. For development, `-dev` serves the files from `web/` on disk instead:

```powershell
.\Tokenizer.exe -dev
```

or, in server mode:

```powershell
go run . -dev
```

Edit a file, then refresh with **F5** / **Ctrl+R** (or close and reopen the desktop window). There is no compile step for HTML/CSS/JS in this mode. Add `-debug` to enable WebView2 developer tools (F12) and the right-click context menu:

```powershell
.\Tokenizer.exe -dev -debug
```

When you are happy with the changes, rebuild the self-contained app so the final executable embeds the new files:

```powershell
.\build.ps1
```

Go changes always require a rebuild; `-dev` only bypasses embedding for the frontend files. The files are UTF-8 — edit them in VS Code, Notepad++, or another UTF-8-aware editor to preserve emoji and special characters.

## How it works

A tokenizer is a pair of static tables plus a small algorithm:

1. **Pre-tokenize.** A regex splits the input into chunks: words, numbers (up to three digits), punctuation runs, whitespace runs, contractions, etc. The patterns are the public tiktoken patterns.
2. **Byte-pair encode each chunk.** A chunk starts as individual UTF-8 bytes. The encoder repeatedly merges the adjacent pair whose merged token has the lowest rank in the vocabulary. This is the same greedy BPE algorithm used by tiktoken.
3. **Look up IDs.** The final merged tokens map directly to integer token IDs.

The vocabulary files are embedded into the Go binary. For example, `cl100k_base` is a 100,256-entry table mapping byte strings to ranks. There is no neural network anywhere in this process.

Implementation notes:

- `internal/tokenizer/` contains the loader, pre-tokenizer, heap-based BPE merger, special-token handling, and API types.
- `github.com/dlclark/regexp2` provides a backtracking regex engine with lookahead support, which Go's standard `regexp` package lacks. The patterns are otherwise direct translations of tiktoken's public patterns.
- `desktop_windows.go` creates the native window, starts the private loopback server, enforces a single instance, and opens external links in the default browser.
- Special tokens are matched before normal pre-tokenization and emitted as single tokens.
- Encoding is loaded lazily on first use and cached for subsequent requests.
- The API accepts up to 100 KB of UTF-8 text per request. The frontend renders at most the first 2,000 tokens to keep the DOM responsive; the token panel scrolls internally, and counts and copied IDs always cover all tokens.

## API

`POST /api/tokenize`

```json
{
  "text": "hello world",
  "encoding": "o200k_base"
}
```

Response:

```json
{
  "encoding": "o200k_base",
  "tokens": [
    {
      "id": 24912,
      "text": "hello",
      "bytes": "68 65 6c 6c 6f",
      "byte_length": 5
    },
    {
      "id": 2375,
      "text": " world",
      "bytes": "20 77 6f 72 6c 64",
      "byte_length": 6
    }
  ],
  "count": 2,
  "characters": 11,
  "bytes": 11
}
```

Other endpoints:

- `GET /api/encodings` — encoding metadata and vocabulary sizes
- `GET /api/health` — health check

Errors use `{"error": "..."}` with an appropriate HTTP status.

## Verification

The test suite compares token IDs against OpenAI's `tiktoken` reference implementation across a fixture corpus, including Unicode, emoji, contractions, whitespace edge cases, special tokens, and all 256 byte values.

```powershell
go test ./...
```

During development this build was checked against tiktoken on 1,500+ randomized text/encoding pairs with no differences. The browser UI passed a 40-check Playwright suite (tabs, keyboard navigation, token IDs, copy actions, render cap, error handling, and mobile layout), and the desktop window was smoke-tested for startup, rendering, API health, and clean shutdown.

## Vocabulary data

The `.tiktoken` files under `vocab/` are the public vocabulary files published by OpenAI:

- <https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken>
- <https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken>
- <https://openaipublic.blob.core.windows.net/encodings/p50k_base.tiktoken>
- <https://openaipublic.blob.core.windows.net/encodings/r50k_base.tiktoken>

The reference implementation is [openai/tiktoken](https://github.com/openai/tiktoken). Vocabulary data is © OpenAI and subject to OpenAI's terms. This project is an independent educational demo and is not affiliated with or endorsed by OpenAI.

The `o200k_base` label mirrors OpenAI's tokenizer UI ("GPT-5.x & O1/3"). Newer closed models may use unpublished encoding variants; this app implements the public vocabularies listed above.
