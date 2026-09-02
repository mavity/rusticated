# kabibi: Dual-Pane File Manager & AI Chat

## Current Architecture & Implementation

kabibi is a terminal-based dual-pane file manager and AI chat application written in Go, powered by the ubbletea framework. 

### 1. Dual-Pane File Manager
*   **Panels**: Implements classic Left/Right directory viewing panels. 
*   **Data Models**: Uses custom ileItem structs loaded via os.ReadDir(), storing attributes like size, modification time, and file mode.
*   **File Operations**: Operations like Copy, Move, and Delete (currently bound to F5, F6, F8) invoke native os commands in background goroutines. They broadcast progress (ileOpProgressMsg) back to the Bubbletea UI via channels to avoid blocking the main event loop.
*   **Modals & Dialogs**: dialog.go drives modal overlays (inputs and confirmations) that block the browser view while capturing input (e.g., creating dirs or confirming deletes).

### 2. Terminal & Shell Integration
*   **Plume Shell**: Includes an embedded u-root coreutils bash-like shell.
*   **Execution**: Simple strings typed without modifier keys go to the shell input prompt. Resulting outputs (shellResultMsg) are rendered into the UI buffer while syncing the active file panel with the process's working directory.

### 3. AI Chat Integration (Gemma 4 / Litert)
*   **Chat View**: Accessible via a quick double Tab press, it overlays the active pane with a chat interface.
*   **Local AI Assets**: Features asynchronous downloading of gemma and litertlm models. 
*   **Streaming**: Conversation tokens stream seamlessly into the chat UI (iTokenMsg) while the local AI model executes, avoiding UI freezes during inference.

### 4. Text Editor (F4)
*   **Core**: Custom text editor built within the UI utilizing chroma/v2 for syntax highlighting.
*   **Clipboard**: Incorporates system clipboard interaction (totto/clipboard) + OSC52 fallbacks.

---

## Planned UX Improvements (FAR Manager Alignment)

To improve workflow efficiency and match the revered UX of FAR Manager, the following modifications are planned:

### Interaction & Flow
*   **F5 / F6 (Copy & Move)**: Moving away from immediate Yes/No confirmation, F5/F6 will now open an editable input dialog pre-filled with the opposite pane�s path, allowing on-the-fly destination adjustments.
*   **Collision Detection / Prompting**: Overwrite operations will no longer silently clobber existing files. Background workers will pause upon collision, throwing a dialog for user resolution (Overwrite, Skip, Append, Overwrite All, Skip All).
*   **Editor Keys**: The editor will utilize F2 to save files (replacing standard Ctrl+S). Pressing Esc will intelligently exit if clean, or launch an unsaved changes confirmation dialog if dirty. Shift+Arrows will handle text selection.
*   **Selection Globbing (+ / -)**: Pressing + or - will launch a "Select by Mask" dialog, allowing users to bulk-select or deselect files via wildcards (e.g., *.go).
*   **Nimble Dialogs**: Dialogs will support single-keystroke resolutions; pressing the highlighted character (e.g., O for Overwrite) will immediately fulfill the action without requiring Tab cycling + Enter. 
*   **Minimal Dialog Layout**: The button layout will transition from verbose block styling to a minimal, centered <Action>   [Cancel] format matching FAR.

### Color Palette & ANSI Fallbacks
*   **Dialogs & Popups**: Migrating to a vivid, high-contrast, and comfortable palette to rapidly center the user's attention.
*   **Editor Vibe**: Transitioning the text editor to a "Light text on Deep Blue" palette. The blue background will be a noticeably deeper and distinct shade compared to the standard panel blue.
*   **ANSI Safe Wrappers**: To ensure visual consistency across *all* terminals, every modern 24-bit/TrueColor sequence used for the UI will be wrapped in flat, guaranteed old-style 16-color ANSI equivalents. If legacy terminals strip or fail on the rich shades, the UI will safely degrade to the standard dark blue wrappers, preserving UX clarity.

# NATIVE

In order to run kabibi native (sometimes faster than building via washmhost) run this in the root directory of the the repo:

`go -C kabibi run .`

For tests:

`go -C kabibi test -v`

-v stands for verbose to see test details.