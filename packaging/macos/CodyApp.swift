// Cody.app is a thin native shell around the cody engine. It launches the
// bundled `cody serve` process on a free loopback port with a private token
// and renders the same web client the browser would, inside a WKWebView.
// Nothing here reimplements the agent: one engine, two clients.
//
// The window is borderless: the web header carries the traffic lights and the
// drag handle, and this file implements edge resizing.
import AppKit
import Security
import WebKit

final class BorderlessWindow: NSWindow {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
}

// EdgeResizeView sits over the web view but only receives events within a few
// pixels of the window edge, so it adds native-feeling resize handles without
// stealing clicks from the UI.
final class EdgeResizeView: NSView {
    private let band: CGFloat = 7
    private var startFrame: NSRect = .zero
    private var startMouse: NSPoint = .zero
    private var edges: Set<Int> = []

    override func hitTest(_ point: NSPoint) -> NSView? {
        let b = bounds
        let near = point.x <= band || point.x >= b.width - band || point.y <= band || point.y >= b.height - band
        return near ? self : nil
    }

    override func resetCursorRects() {
        let b = bounds
        let c = band
        addCursorRect(NSRect(x: 0, y: c, width: c, height: b.height - 2 * c), cursor: .resizeLeftRight)
        addCursorRect(NSRect(x: b.width - c, y: c, width: c, height: b.height - 2 * c), cursor: .resizeLeftRight)
        addCursorRect(NSRect(x: c, y: 0, width: b.width - 2 * c, height: c), cursor: .resizeUpDown)
        addCursorRect(NSRect(x: c, y: b.height - c, width: b.width - 2 * c, height: c), cursor: .resizeUpDown)
    }

    override func mouseDown(with event: NSEvent) {
        guard let win = window else { return }
        startFrame = win.frame
        startMouse = NSEvent.mouseLocation
        let p = convert(event.locationInWindow, from: nil)
        edges = []
        if p.x <= band { edges.insert(0) }
        if p.x >= bounds.width - band { edges.insert(1) }
        if p.y <= band { edges.insert(2) }
        if p.y >= bounds.height - band { edges.insert(3) }
    }

    override func mouseDragged(with event: NSEvent) {
        guard let win = window else { return }
        let now = NSEvent.mouseLocation
        let dx = now.x - startMouse.x
        let dy = now.y - startMouse.y
        var f = startFrame
        let minW = win.minSize.width
        let minH = win.minSize.height
        if edges.contains(1) { f.size.width = max(minW, startFrame.width + dx) }
        if edges.contains(0) {
            let w = max(minW, startFrame.width - dx)
            f.origin.x = startFrame.maxX - w
            f.size.width = w
        }
        if edges.contains(3) { f.size.height = max(minH, startFrame.height + dy) }
        if edges.contains(2) {
            let h = max(minH, startFrame.height - dy)
            f.origin.y = startFrame.maxY - h
            f.size.height = h
        }
        win.setFrame(f, display: true)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKScriptMessageHandler {
    private var window: BorderlessWindow!
    private var webView: WKWebView!
    private var server: Process?
    // The server's stdin. Only this process holds the write end, so the OS
    // closes it when the app exits for any reason (even a crash or force quit)
    // and `serve --watch-stdin` stops. Keep it alive while the server runs.
    private var serverLifeline: Pipe?
    private var workspace: URL?
    private var lastLog = ""
    private var logHandle: FileHandle?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        buildMenu()
        createWindow()
        guard let dir = resolveWorkspace() else {
            NSApp.terminate(nil)
            return
        }
        workspace = dir
        startServer(in: dir)
        NSApp.activate(ignoringOtherApps: true)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationWillTerminate(_ notification: Notification) { stopServer() }

    // MARK: - Window and menu

    private func createWindow() {
        let rect = NSRect(x: 0, y: 0, width: 1180, height: 820)
        window = BorderlessWindow(
            contentRect: rect,
            styleMask: [.borderless, .resizable, .miniaturizable, .closable],
            backing: .buffered,
            defer: false
        )
        window.isOpaque = false
        window.backgroundColor = .clear
        window.hasShadow = true
        window.minSize = NSSize(width: 620, height: 420)
        window.setFrameAutosaveName("CodyWindow")

        let container = NSView(frame: rect)
        container.wantsLayer = true
        container.layer?.cornerRadius = 12
        container.layer?.masksToBounds = true
        container.layer?.backgroundColor = NSColor(calibratedRed: 0.067, green: 0.078, blue: 0.075, alpha: 1).cgColor

        let config = WKWebViewConfiguration()
        config.userContentController.add(self, name: "cody")
        webView = WKWebView(frame: container.bounds, configuration: config)
        webView.autoresizingMask = [.width, .height]
        webView.navigationDelegate = self
        webView.underPageBackgroundColor = NSColor(calibratedRed: 0.067, green: 0.078, blue: 0.075, alpha: 1)
        container.addSubview(webView)

        let edges = EdgeResizeView(frame: container.bounds)
        edges.autoresizingMask = [.width, .height]
        container.addSubview(edges)

        window.contentView = container
        window.center()
        window.makeKeyAndOrderFront(nil)
    }

    private func buildMenu() {
        let mainMenu = NSMenu()
        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)
        let appMenu = NSMenu()
        appItem.submenu = appMenu
        appMenu.addItem(withTitle: "About cody", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Open Folder…", action: #selector(openFolder), keyEquivalent: "o")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Close Window", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        appMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        appMenu.addItem(withTitle: "Zoom", action: #selector(NSWindow.performZoom(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Quit cody", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")

        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editItem.submenu = editMenu
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")

        NSApp.mainMenu = mainMenu
    }

    // MARK: - Window controls from the web header

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.name == "cody", let action = message.body as? String else { return }
        switch action {
        case "close":
            window.performClose(nil)
        case "minimize":
            window.performMiniaturize(nil)
        case "zoom":
            window.performZoom(nil)
        case "drag":
            if let event = NSApp.currentEvent {
                window.performDrag(with: event)
            }
        case "chooseFolder":
            chooseFolderFromWeb()
        default:
            break
        }
    }

    private var choosingFolder = false

    private func chooseFolderFromWeb() {
        if choosingFolder { return }
        choosingFolder = true
        defer { choosingFolder = false }
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = true
        panel.prompt = "Choose"
        panel.message = "Choose a project folder for cody"
        panel.directoryURL = FileManager.default.homeDirectoryForCurrentUser
        let path = panel.runModal() == .OK ? panel.url?.path : nil
        let literal = path.map { jsString($0) } ?? "null"
        webView.evaluateJavaScript("window.codyFolderChosen && window.codyFolderChosen(\(literal));", completionHandler: nil)
    }

    private func jsString(_ s: String) -> String {
        var out = "\""
        for scalar in s.unicodeScalars {
            switch scalar {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            default:
                if scalar.value < 0x20 {
                    out += String(format: "\\u%04x", scalar.value)
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        return out + "\""
    }

    @objc private func openFolder() {
        guard let dir = chooseFolder() else { return }
        UserDefaults.standard.set(dir.path, forKey: "workspace")
        workspace = dir
        startServer(in: dir)
    }

    // MARK: - Workspace

    private func resolveWorkspace() -> URL? {
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: "--workspace"), i + 1 < args.count {
            return URL(fileURLWithPath: args[i + 1])
        }
        if let env = ProcessInfo.processInfo.environment["CODY_WORKSPACE"], !env.isEmpty {
            return URL(fileURLWithPath: env)
        }
        if let saved = UserDefaults.standard.string(forKey: "workspace"),
           FileManager.default.fileExists(atPath: saved) {
            return URL(fileURLWithPath: saved)
        }
        return chooseFolder()
    }

    private func chooseFolder() -> URL? {
        while true {
            let panel = NSOpenPanel()
            panel.canChooseFiles = false
            panel.canChooseDirectories = true
            panel.allowsMultipleSelection = false
            panel.prompt = "Open"
            panel.message = "Choose a workspace for cody"
            guard panel.runModal() == .OK, let url = panel.url else { return nil }
            if url.path == NSHomeDirectory() {
                alert("The home directory is not a workspace. Pick a project folder.")
                continue
            }
            UserDefaults.standard.set(url.path, forKey: "workspace")
            return url
        }
    }

    // MARK: - Server process

    private func startServer(in dir: URL) {
        stopServer()
        guard let binary = bundledBinary() else {
            alert("The cody binary is missing from the app bundle.")
            return
        }
        let port = freePort()
        let token = randomToken()
        let proc = Process()
        proc.executableURL = binary
        proc.currentDirectoryURL = dir
        proc.arguments = ["serve", "--addr", "127.0.0.1:\(port)", "--token", token, "--watch-stdin"]
        let lifeline = Pipe()
        proc.standardInput = lifeline
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:" + (env["PATH"] ?? "")
        proc.environment = env

        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = pipe
        // Drain continuously: an unread pipe would eventually block the child,
        // and the log is the only way to see startup problems from the GUI.
        startLogDrain(pipe: pipe)
        proc.terminationHandler = { [weak self] p in
            guard let self else { return }
            DispatchQueue.main.async {
                self.stopLogDrain()
                if p.terminationStatus != 0 {
                    self.alert("cody serve exited with status \(p.terminationStatus).\n\n\(self.lastLog.suffix(800))")
                }
            }
        }

        do {
            try proc.run()
        } catch {
            alert("Could not start cody serve: \(error.localizedDescription)")
            return
        }
        server = proc
        serverLifeline = lifeline
        window.title = "cody — \(dir.lastPathComponent)"
        // Something readable immediately; the engine can take a while to boot
        // on a busy machine (Ollama loading a model, for example).
        webView.loadHTMLString(Self.startingHTML, baseURL: nil)

        guard let url = URL(string: "http://127.0.0.1:\(port)/?token=\(token)") else { return }
        waitForPort(port, process: proc) { [weak self] up in
            guard let self else { return }
            if up {
                self.webView.load(URLRequest(url: url))
            } else {
                self.alert("cody serve did not come up on port \(port).\n\n\(self.lastLog.suffix(500))")
            }
        }
    }

    private static let startingHTML = """
    <!doctype html><html><head><meta charset="utf-8"><style>
    html,body{height:100%;margin:0;background:#0e0e0e;color:#8a8a8a;
      font:14px ui-monospace,Menlo,monospace;display:flex;align-items:center;justify-content:center}
    .box{text-align:center}
    .face{color:#4cc9f0;font-size:16px;letter-spacing:2px}
    </style></head><body><div class="box">
    <div class="face">[ ●  ● ]</div>
    <div style="margin-top:10px">starting cody engine…</div>
    </div>
    <script>
    const f=["[ ●  ● ]","[●  ●  ]","[-  -  ]","[ ●  ● ]","[  ●  ●]","[  -  -]","[ ●  ● ]","[ -  - ]"];
    let i=0;setInterval(()=>{i=(i+1)%f.length;document.querySelector(".face").textContent=f[i]},120);
    </script></body></html>
    """

    private func stopServer() {
        if let proc = server, proc.isRunning {
            proc.terminate()
            let deadline = Date().addingTimeInterval(2)
            while proc.isRunning && Date() < deadline { usleep(50_000) }
            if proc.isRunning { kill(proc.processIdentifier, SIGKILL) }
        }
        server = nil
        serverLifeline = nil
        stopLogDrain()
    }

    private func startLogDrain(pipe: Pipe) {
        let dir = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("cody-serve.log")
        FileManager.default.createFile(atPath: url.path, contents: nil)
        let handle = try? FileHandle(forWritingTo: url)
        handle?.seekToEndOfFile()
        logHandle = handle
        lastLog = ""
        pipe.fileHandleForReading.readabilityHandler = { [weak self] fh in
            let data = fh.availableData
            if data.isEmpty { return }
            handle?.write(data)
            guard let self else { return }
            DispatchQueue.main.async {
                self.lastLog += String(data: data, encoding: .utf8) ?? ""
            }
        }
    }

    private func stopLogDrain() {
        logHandle?.closeFile()
        logHandle = nil
    }

    private func bundledBinary() -> URL? {
        if let url = Bundle.main.url(forResource: "cody", withExtension: nil),
           FileManager.default.isExecutableFile(atPath: url.path) {
            return url
        }
        if let env = ProcessInfo.processInfo.environment["CODY_BIN"],
           FileManager.default.isExecutableFile(atPath: env) {
            return URL(fileURLWithPath: env)
        }
        return nil
    }

    private func waitForPort(_ port: Int, process: Process, completion: @escaping (Bool) -> Void) {
        DispatchQueue.global().async {
            // Wait while the child is alive; a busy Ollama can delay startup by
            // minutes. Only give up if the process dies or this cap is hit.
            for _ in 0..<1440 {
                if Self.portOpen(port) {
                    DispatchQueue.main.async { completion(true) }
                    return
                }
                if !process.isRunning {
                    break
                }
                usleep(250_000)
            }
            DispatchQueue.main.async { completion(false) }
        }
    }

    private static func portOpen(_ port: Int) -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        if fd < 0 { return false }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = UInt16(port).bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let result = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        return result == 0
    }

    private func freePort() -> Int {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        if fd < 0 { return 8765 }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        if bound != 0 { return 8765 }
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        var out = sockaddr_in()
        let named = withUnsafeMutablePointer(to: &out) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(fd, $0, &len)
            }
        }
        if named != 0 { return 8765 }
        return Int(UInt16(bigEndian: out.sin_port))
    }

    private func randomToken() -> String {
        var bytes = [UInt8](repeating: 0, count: 16)
        if SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) != errSecSuccess {
            return UUID().uuidString.replacingOccurrences(of: "-", with: "")
        }
        return bytes.map { String(format: "%02x", $0) }.joined()
    }

    private func alert(_ message: String) {
        let a = NSAlert()
        a.messageText = "cody"
        a.informativeText = message
        a.alertStyle = .warning
        a.addButton(withTitle: "OK")
        a.runModal()
    }

    // MARK: - Navigation feedback

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        alert("Could not load the cody UI: \(error.localizedDescription)")
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
