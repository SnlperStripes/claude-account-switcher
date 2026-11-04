// Local macOS UI for the reviewed Go backend. No credentials or networking here.
import AppKit
import SwiftUI
import UserNotifications

struct AccountRow: Codable, Identifiable {
    var id: String; var name: String; var plan: String; var problem: String; var needsLogin: Bool
    var hasUsage: Bool; var session: Double; var weekly: Double
    var sessionReset: Double; var weeklyReset: Double; var checkedAt: Double; var stale: Bool
    var peak: Double { max(session, weekly) }
}
struct PanelState: Codable {
    var accounts: [AccountRow] = []; var active = ""; var autoSwitch = false
    var spendWeekly = false; var threshold: Double = 96; var status = ""; var note = ""
    var busy = false; var autostart = false; var version = "development"
    var update = ""
}

@MainActor final class PanelStore: ObservableObject {
    @Published var state = PanelState()
    @Published var settings = false { didSet { layoutChanged?() } }
    @Published var panelWidth: CGFloat = 440
    @Published var panelHeight: CGFloat = 608
    @Published var refreshing = false
    @Published var now = Date()
    private let defaults: UserDefaults
    @Published var showRemaining: Bool {
        didSet { defaults.set(showRemaining, forKey: "showRemaining") }
    }
    @Published var hideNames: Bool {
        didSet { defaults.set(hideNames, forKey: "hideNames"); changed?() }
    }
    @Published var showPercentage: Bool {
        didSet { defaults.set(showPercentage, forKey: "showPercentage"); changed?() }
    }
    @Published var alerts: Bool
    var changed: (() -> Void)?
    var layoutChanged: (() -> Void)?
    var previous: [String: AccountRow] = [:]
    var delivered = Set<String>()
    init(defaults: UserDefaults = .standard) {
        self.defaults=defaults
        showRemaining=defaults.bool(forKey:"showRemaining")
        hideNames=defaults.bool(forKey:"hideNames")
        showPercentage=defaults.object(forKey:"showPercentage") as? Bool ?? true
        alerts=defaults.bool(forKey:"usageAlerts")
    }
    func send(_ type: String, id: String = "", enabled: Bool = false, value: Double = 0) {
        let action: [String: Any] = ["type": type, "id": id, "enabled": enabled, "value": value]
        if let data = try? JSONSerialization.data(withJSONObject: action) {
            FileHandle.standardOutput.write(data + Data([10]))
        }
    }
    func apply(_ next: PanelState) {
        let stamp = Date().timeIntervalSince1970
        for account in next.accounts where account.hasUsage && !account.stale && account.problem.isEmpty {
            if alerts, let old = previous[account.id] {
                for (window, used, before, reset) in [
                    ("five-hour", account.session, old.session, account.sessionReset),
                    ("weekly", account.weekly, old.weekly, account.weeklyReset)
                ] {
                    for threshold in [85.0, 95.0, 100.0] where before < threshold && used >= threshold {
                        let key = "\(account.id)-\(window)-\(reset)-\(threshold)"
                        if delivered.insert(key).inserted {
                            notify("Usage warning", "\(displayName(account)): \(Int(threshold))% of the \(window) allowance used.")
                        }
                    }
                }
                for (window, reset) in [("five-hour", old.sessionReset), ("weekly", old.weeklyReset)] where reset > 0 && reset <= stamp {
                    let key = "reset-\(account.id)-\(window)-\(reset)"
                    if delivered.insert(key).inserted { notify("Usage window reset", "\(displayName(account)): the \(window) reset time has passed.") }
                }
            }
            previous[account.id] = account
        }
        state = next; refreshing = false; now = Date(); changed?()
    }
    func notify(_ title: String, _ body: String) {
        let content = UNMutableNotificationContent(); content.title = title; content.body = body
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
    }
    func setAlerts(_ enabled: Bool) {
        if !enabled { alerts = false; defaults.set(false, forKey: "usageAlerts"); return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, _ in
            DispatchQueue.main.async { self.alerts = granted; self.defaults.set(granted, forKey: "usageAlerts") }
        }
    }
    func displayName(_ account: AccountRow) -> String {
        hideNames ? "Account \((state.accounts.firstIndex(where: {$0.id == account.id}) ?? 0) + 1)" : account.name
    }
    func confirm(_ title: String, message: String, action: String, id: String = "") {
        let alert = NSAlert(); alert.messageText = title; alert.informativeText = message
        alert.addButton(withTitle: "Continue"); alert.addButton(withTitle: "Cancel")
        if alert.runModal() == .alertFirstButtonReturn { send(action, id: id) }
    }
}

func usageTint(_ percent: Double) -> Color { percent >= 100 ? .red : percent >= 85 ? .orange : .green }
func resetClock(_ seconds: Double, now: Date) -> String {
    guard seconds > 0 else { return "Not reported" }
    let date = Date(timeIntervalSince1970: seconds)
    let format = DateFormatter(); format.locale = Locale(identifier: "en_US_POSIX")
    format.dateFormat = Calendar.current.isDate(date, inSameDayAs: now) ? "h:mm a" : "EEE d MMM, h:mm a"
    return format.string(from: date)
}
func remaining(_ seconds: Double, now: Date) -> String {
    guard seconds > 0 else { return "Reset not reported" }
    let delta = seconds - now.timeIntervalSince1970
    guard delta > 0 else { return "Reset due · refresh to verify" }
    let minutes = Int(ceil(delta / 60)); let hours = minutes / 60
    if minutes >= 1440 { return "Resets in \(minutes / 1440)d \((minutes % 1440) / 60)h" }
    return hours == 0 ? "Resets in \(minutes)m" : "Resets in \(hours)h \(minutes % 60)m"
}
func freshness(_ checked: Double, now: Date) -> String {
    guard checked > 0 else { return "Not checked yet" }
    let seconds = max(0, Int(now.timeIntervalSince1970 - checked))
    return seconds < 60 ? "Updated just now" : "Updated \(seconds / 60)m ago"
}

struct UsageMetric: View {
    let title: String; let symbol: String; let percent: Double; let reset: Double
    let now: Date; let showRemaining: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 5) {
                Image(systemName: symbol).foregroundStyle(.secondary)
                Text(title).foregroundStyle(.secondary)
                Spacer(minLength: 2)
                Text("\(Int((showRemaining ? 100 - percent : percent).rounded()))% \(showRemaining ? "left" : "used")").fontWeight(.medium)
            }.font(.system(size: 11))
            GeometryReader { geometry in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.primary.opacity(0.10))
                    Capsule().fill(usageTint(percent)).frame(width: geometry.size.width * min(100, max(0, percent)) / 100)
                }
            }.frame(height: 5).accessibilityLabel("\(title): \(Int(percent)) percent used")
            Text(remaining(reset, now: now)).font(.system(size: 11)).foregroundStyle(.secondary)
            Text(resetClock(reset, now: now)).font(.system(size: 11)).foregroundStyle(.secondary)
        }.monospacedDigit()
    }
}

struct AccountCard: View {
    @ObservedObject var store: PanelStore
    let account: AccountRow
    var active: Bool { store.state.active == account.id }
    var status: (String, String, Color) {
        if account.needsLogin { return ("key.fill", "Sign-in required", .orange) }
        if !account.problem.isEmpty { return ("wifi.exclamationmark", account.problem, .orange) }
        if account.stale { return ("exclamationmark.triangle", "Usage may be stale", .orange) }
        if !account.hasUsage { return ("clock", "Waiting for usage", .secondary) }
        if account.peak >= 100 { return ("minus.circle", "Limit reached", .red) }
        if account.peak >= 85 { return ("exclamationmark.triangle", "Nearly full", .orange) }
        return ("checkmark.circle", "Ready", .green)
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 11) {
            HStack(spacing: 9) {
                Text(String(store.displayName(account).prefix(1)).uppercased())
                    .font(.system(size: 13, weight: .semibold)).frame(width: 29, height: 29)
                    .background(Color.primary.opacity(0.06), in: RoundedRectangle(cornerRadius: 8))
                Text(store.displayName(account)).font(.system(size: 13, weight: .semibold)).lineLimit(1)
                Text(account.plan).font(.system(size: 10, weight: .medium)).foregroundStyle(.secondary)
                Spacer(minLength: 4)
                if active {
                    Label("Active", systemImage: "checkmark.circle.fill").font(.system(size: 11)).foregroundStyle(.blue)
                } else {
                    Button("Switch") {
                        store.confirm("Switch to \(store.displayName(account))?", message: "Claude Desktop will restart. Finish or save any running work first. Local Code history is shared across accounts by this switcher.", action: "switch", id: account.id)
                    }.controlSize(.small).disabled(store.state.busy)
                }
            }
            if account.hasUsage {
                HStack(alignment: .top, spacing: 18) {
                    UsageMetric(title: "Five hours", symbol: "clock", percent: account.session, reset: account.sessionReset, now: store.now, showRemaining: store.showRemaining)
                    UsageMetric(title: "Weekly", symbol: "calendar", percent: account.weekly, reset: account.weeklyReset, now: store.now, showRemaining: store.showRemaining)
                }
            }
            HStack {
                Label(status.1, systemImage: status.0).foregroundStyle(status.2)
                Spacer()
                Text(freshness(account.checkedAt, now: store.now)).foregroundStyle(.secondary)
            }.font(.system(size: 10))
        }
        .padding(12)
        .background(active ? Color.blue.opacity(0.08) : Color.primary.opacity(0.025), in: RoundedRectangle(cornerRadius: 11))
        .overlay(RoundedRectangle(cornerRadius: 11).strokeBorder(active ? Color.blue.opacity(0.18) : Color.primary.opacity(0.06), lineWidth: 1))
    }
}

struct SettingsView: View {
    @ObservedObject var store: PanelStore
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack { Button {store.settings=false} label: {Label("Accounts",systemImage:"chevron.left")}; Spacer(); Text("Settings").fontWeight(.semibold) }
            GroupBox("Display") {
                VStack(alignment:.leading,spacing:9) {
                    Toggle("Show remaining percentages",isOn:$store.showRemaining)
                    Toggle("Hide account names",isOn:$store.hideNames)
                    Toggle("Show percentage in menu bar",isOn:$store.showPercentage)
                    Text("Menu-bar percentage uses the fuller of your session and weekly limits.").font(.caption).foregroundStyle(.secondary)
                }.padding(6).frame(maxWidth:.infinity,alignment:.leading)
            }
            GroupBox("Notifications") {
                Toggle("Usage warnings and reset alerts",isOn:Binding(get:{store.alerts},set:{store.setAlerts($0)})).padding(6).frame(maxWidth:.infinity,alignment:.leading)
                Text("Alerts at 85%, 95% and 100%. macOS permission is requested only when enabled.").font(.caption).foregroundStyle(.secondary).padding(.bottom,5)
            }
            GroupBox("Switching") {
                VStack(alignment:.leading,spacing:9) {
                    Toggle("Auto-switch when nearly full",isOn:Binding(get:{store.state.autoSwitch},set:{store.send("auto",enabled:$0)}))
                    Picker("Switch threshold",selection:Binding(get:{store.state.threshold},set:{store.send("threshold",value:$0)})) {
                        ForEach([90.0,94,96,98],id:\.self){Text("\(Int($0))%").tag($0)}
                    }
                    Toggle("Use expiring weekly allowance",isOn:Binding(get:{store.state.spendWeekly},set:{store.send("weekly",enabled:$0)}))
                    Toggle("Start at login",isOn:Binding(get:{store.state.autostart},set:{store.send("autostart",enabled:$0)}))
                }.padding(6).frame(maxWidth:.infinity,alignment:.leading)
            }
            GroupBox("Local history") {
                VStack(alignment:.leading,spacing:8) {
                    Label("Shared across all local accounts",systemImage:"person.2").font(.caption)
                    Text("Switching imports Code session entries and project metadata. Per-project sharing is not available in this version.").font(.caption).foregroundStyle(.secondary)
                    Button("Import all local Code history…") {store.confirm("Import local history?",message:"This copies session entries and project metadata from all local accounts and restarts Claude Desktop.",action:"import")}.disabled(store.state.busy)
                }.padding(6).frame(maxWidth:.infinity,alignment:.leading)
            }
            HStack {
                Button {store.send("folder")} label:{Label("Data folder",systemImage:"folder")}
                Spacer(); Button("Quit switcher"){store.send("quit")}
            }
            if !store.state.update.isEmpty {
                Button("Version \(store.state.update) available — view releases") {store.send("releases")}
            }
            Text("Local build \(store.state.version) · Times follow your Mac’s timezone").font(.caption2).foregroundStyle(.secondary)
        }.font(.system(size:12)).padding(16)
    }
}

struct OverviewView: View {
    @ObservedObject var store: PanelStore
    var body: some View {
        VStack(spacing:0) {
            if store.settings { ScrollView { SettingsView(store:store) } } else {
                HStack {
                    VStack(alignment:.leading,spacing:3) {
                        Text("Claude accounts").font(.system(size:15,weight:.semibold))
                        Text("\(store.state.accounts.count) accounts · Local time").font(.system(size:11)).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button {store.refreshing=true;store.send("refresh")} label:{Image(systemName:"arrow.clockwise")}
                        .disabled(store.refreshing || store.state.busy).help("Refresh usage; server rate limits still apply")
                    Button {store.settings=true} label:{Image(systemName:"slider.horizontal.3")}.help("Settings")
                }.buttonStyle(.borderless).padding(16)
                if !store.state.status.isEmpty || !store.state.note.isEmpty {
                    Label(store.state.status.isEmpty ? store.state.note : store.state.status,systemImage:store.state.busy ? "arrow.triangle.2.circlepath" : "info.circle")
                        .font(.caption).foregroundStyle(.secondary).lineLimit(3).frame(maxWidth:.infinity,alignment:.leading).padding(.horizontal,16).padding(.bottom,10)
                }
                ScrollView {
                    VStack(spacing:8) {
                        if store.state.accounts.isEmpty {Text("Open Claude Desktop and sign in to see your accounts.").foregroundStyle(.secondary).padding(30)}
                        ForEach(store.state.accounts){AccountCard(store:store,account:$0)}
                    }.padding(.horizontal,10).padding(.bottom,10)
                }.frame(maxWidth:.infinity,maxHeight:.infinity)
                Divider()
                HStack {
                    Button {store.confirm("Add another account?",message:"Your current sign-in is saved locally, then Claude Desktop restarts at the login screen. Finish any active work first.",action:"add")} label:{Label("Add account",systemImage:"plus")}.disabled(store.state.busy)
                    Spacer()
                    Label("All local history shared",systemImage:"person.2").font(.system(size:10)).foregroundStyle(.secondary)
                }.buttonStyle(.borderless).padding(14)
            }
        }.frame(width:store.panelWidth,height:store.panelHeight).background(Color(nsColor:.windowBackgroundColor))
    }
}

// Coordinates are screen points, including displays left of/below the primary.
func anchoredPanelFrame(anchor: NSRect, visible: NSRect, desired: NSSize) -> NSRect {
    let gap:CGFloat=8
    let width=max(1,min(desired.width,visible.width-2*gap))
    let availableBelow=max(1,min(anchor.minY,visible.maxY)-visible.minY-2*gap)
    let height=max(1,min(desired.height,availableBelow))
    let x=max(visible.minX+gap,min(anchor.midX-width/2,visible.maxX-gap-width))
    let y=max(visible.minY+gap,min(anchor.minY-gap-height,visible.maxY-gap-height))
    return NSRect(x:x,y:y,width:width,height:height)
}

final class AccountPanel: NSPanel {
    override var canBecomeKey:Bool { true }
    override var canBecomeMain:Bool { false }
}

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    let store=PanelStore(); var item:NSStatusItem!; var panel:AccountPanel!; var timer:Timer?
    var localMonitor:Any?; var globalMonitor:Any?; var resignObserver:NSObjectProtocol?
    var spaceObserver:NSObjectProtocol?; var screenObserver:NSObjectProtocol?
    var initialShowPending=false
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        UNUserNotificationCenter.current().delegate=self
        item=NSStatusBar.system.statusItem(withLength:NSStatusItem.variableLength)
        item.button?.target=self;item.button?.action=#selector(toggle)
        panel=AccountPanel(contentRect:NSRect(x:0,y:0,width:440,height:608),styleMask:[.borderless,.nonactivatingPanel],backing:.buffered,defer:false)
        panel.contentViewController=NSHostingController(rootView:OverviewView(store:store))
        panel.isReleasedWhenClosed=false
        panel.level = .statusBar
        panel.hasShadow=true
        panel.isOpaque=false
        panel.backgroundColor = .clear
        panel.hidesOnDeactivate=false
        panel.collectionBehavior=[.moveToActiveSpace,.fullScreenAuxiliary]
        panel.contentView?.wantsLayer=true
        panel.contentView?.layer?.cornerRadius=14
        panel.contentView?.layer?.masksToBounds=true
        initialShowPending=CommandLine.arguments.contains("--show")
        store.changed = { [weak self] in
            guard let self=self else{return}
            self.updateIcon()
            if self.initialShowPending {self.initialShowPending=false;self.showPanel()}
            else if self.panel.isVisible {self.positionPanel()}
        }
        store.layoutChanged = { [weak self] in if self?.panel.isVisible == true {self?.positionPanel()} }
        resignObserver=NotificationCenter.default.addObserver(forName:NSApplication.didResignActiveNotification,object:nil,queue:.main){[weak self] _ in Task { @MainActor in self?.hidePanel() } }
        spaceObserver=NSWorkspace.shared.notificationCenter.addObserver(forName:NSWorkspace.activeSpaceDidChangeNotification,object:nil,queue:.main){[weak self] _ in Task { @MainActor in self?.hidePanel() } }
        screenObserver=NotificationCenter.default.addObserver(forName:NSApplication.didChangeScreenParametersNotification,object:nil,queue:.main){[weak self] _ in Task { @MainActor in self?.hidePanel() } }
        timer=Timer.scheduledTimer(withTimeInterval:15,repeats:true){[weak self] _ in
            Task { @MainActor in self?.store.now=Date();self?.updateIcon() }
        }
        updateIcon()
        let currentStore=store
        DispatchQueue.global(qos:.utility).async {
            while let line=readLine() {
                guard let data=line.data(using:.utf8),let state=try? JSONDecoder().decode(PanelState.self,from:data) else {continue}
                DispatchQueue.main.async {currentStore.apply(state)}
            }
            DispatchQueue.main.async {NSApp.terminate(nil)}
        }
        store.send("ready")
    }
    func updateIcon() {
        guard let button=item?.button else{return}
        let active=store.state.accounts.first{$0.id==store.state.active}
        let peak=active?.peak ?? -1
        let symbol=store.state.busy ? "arrow.triangle.2.circlepath" : peak>=100 ? "exclamationmark.circle" : "circle.dotted.circle"
        let image=NSImage(systemSymbolName:symbol,accessibilityDescription:"Claude account usage")
        image?.isTemplate=true;button.image=image;button.imagePosition = .imageLeading
        button.title=store.showPercentage && peak>=0 ? " \(Int(peak.rounded()))%" : ""
        button.toolTip=active.map{"\(store.displayName($0)) · Session \(Int($0.session))% · Weekly \(Int($0.weekly))%"} ?? "Claude accounts"
    }
    @objc func toggle() {
        if panel.isVisible {hidePanel()} else {showPanel()}
    }
    func positionPanel() {
        guard let button=item.button,let window=button.window,let screen=window.screen ?? NSScreen.main else{return}
        let anchor=window.convertToScreen(button.convert(button.bounds,to:nil))
        let banner=store.state.status.isEmpty && store.state.note.isEmpty ? 0.0 : 50.0
        let desiredHeight=store.settings ? 660.0 : min(660.0,Double(max(1,store.state.accounts.count))*160+128+banner)
        let frame=anchoredPanelFrame(anchor:anchor,visible:screen.visibleFrame,desired:NSSize(width:440,height:desiredHeight))
        store.panelWidth=frame.width;store.panelHeight=frame.height
        panel.setFrame(frame,display:true)
    }
    func showPanel() {
        positionPanel()
        panel.makeKeyAndOrderFront(nil)
        installDismissalMonitors()
    }
    func hidePanel() {
        panel?.orderOut(nil)
        if let monitor=localMonitor {NSEvent.removeMonitor(monitor);localMonitor=nil}
        if let monitor=globalMonitor {NSEvent.removeMonitor(monitor);globalMonitor=nil}
    }
    func installDismissalMonitors() {
        guard localMonitor == nil,globalMonitor == nil else{return}
        // Observe only mouse-down events while visible. Do not log or consume
        // clicks in other apps; the click still reaches its original target.
        globalMonitor=NSEvent.addGlobalMonitorForEvents(matching:[.leftMouseDown,.rightMouseDown,.otherMouseDown]){[weak self] _ in if NSApp.modalWindow == nil {self?.hidePanel()} }
        localMonitor=NSEvent.addLocalMonitorForEvents(matching:[.leftMouseDown,.rightMouseDown,.otherMouseDown,.keyDown]){[weak self] event in
            guard let self=self else{return event}
            if NSApp.modalWindow != nil {return event}
            if event.type == .keyDown {
                if event.keyCode == 53 {self.hidePanel();return nil}
            } else if event.window !== self.panel && event.window !== self.item.button?.window && NSApp.modalWindow == nil {
                self.hidePanel()
            }
            return event
        }
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }
}

@main enum PanelApplication {
    @MainActor static func main() {
        if CommandLine.arguments.contains("--test-layout") {
            for visible in [NSRect(x:0,y:0,width:1440,height:875),NSRect(x:-1280,y:120,width:1280,height:650),NSRect(x:0,y:-900,width:900,height:450)] {
                for anchorX in [visible.minX,visible.midX,visible.maxX-20] {
                    for height in [288.0,608.0,1200.0] {
                        let anchor=NSRect(x:anchorX,y:visible.maxY,width:20,height:24)
                        let frame=anchoredPanelFrame(anchor:anchor,visible:visible,desired:NSSize(width:440,height:height))
                        precondition(visible.contains(frame),"Panel escaped visible display")
                        precondition(frame.maxY <= anchor.minY,"Panel covered menu bar")
                    }
                }
            }
            print("27 screen-edge and multi-display layout cases passed")
            return
        }
        if let index=CommandLine.arguments.firstIndex(of:"--render"), CommandLine.arguments.count>index+1 {
            let app=NSApplication.shared
            app.setActivationPolicy(.accessory)
            let suite="claude-switcher-render-\(UUID().uuidString)"
            let defaults=UserDefaults(suiteName:suite)!
            defer { defaults.removePersistentDomain(forName:suite) }
            let store=PanelStore(defaults:defaults); let now=Date().timeIntervalSince1970
            store.state.accounts=[
                AccountRow(id:"a",name:"Personal",plan:"Team",problem:"",needsLogin:false,hasUsage:true,session:24,weekly:61,sessionReset:now+8040,weeklyReset:now+172800,checkedAt:now,stale:false),
                AccountRow(id:"b",name:"Work",plan:"Team",problem:"",needsLogin:false,hasUsage:true,session:96,weekly:38,sessionReset:now+4080,weeklyReset:now+345600,checkedAt:now-120,stale:false),
                AccountRow(id:"c",name:"Studio",plan:"Team",problem:"",needsLogin:false,hasUsage:true,session:100,weekly:17,sessionReset:now+11520,weeklyReset:now+604800,checkedAt:now-240,stale:false)
            ];store.state.active="a";store.now=Date(timeIntervalSince1970:now)
            store.settings=CommandLine.arguments.contains("--settings")
            if store.settings {store.panelHeight=660}
            if CommandLine.arguments.contains("--short-screen") {store.panelHeight=400}
            let view=NSHostingView(rootView:OverviewView(store:store))
            let size=view.fittingSize
            view.frame=NSRect(origin:.zero,size:size)
            let window=NSWindow(contentRect:view.frame,styleMask:[.borderless],backing:.buffered,defer:false)
            window.contentView=view
            view.layoutSubtreeIfNeeded()
            if let image=view.bitmapImageRepForCachingDisplay(in:view.bounds) {
                view.cacheDisplay(in:view.bounds,to:image)
                if let data=image.representation(using:.png,properties:[:]) {
                    try? data.write(to:URL(fileURLWithPath:CommandLine.arguments[index+1]))
                }
            }
            return
        }
        let app=NSApplication.shared
        let delegate=AppDelegate()
        app.delegate=delegate
        app.run()
        withExtendedLifetime(delegate) {}
    }
}
