#if os(iOS)
import GameController
import SwiftUI
import UIKit

final class KeyInputProxy: UIView, UITextInput {
    private static let onScreenKeysHeight: CGFloat = 100

    weak var controller: SessionController?
    weak var screen: RemoteScreenUIView?
    private var accessoryHost: UIHostingController<KeyboardAccessoryBar>?
    private var isOnScreenKeyboardUp = false

    var autocorrectionType: UITextAutocorrectionType = .no
    var autocapitalizationType: UITextAutocapitalizationType = .none
    var spellCheckingType: UITextSpellCheckingType = .no
    var smartQuotesType: UITextSmartQuotesType = .no
    var smartDashesType: UITextSmartDashesType = .no
    var smartInsertDeleteType: UITextSmartInsertDeleteType = .no
    var keyboardType: UIKeyboardType = .default

    @available(iOS 17, *)
    var inlinePredictionType: UITextInlinePredictionType {
        get { .no }
        set {}
    }

    @available(iOS 18, *)
    var writingToolsBehavior: UIWritingToolsBehavior {
        get { .none }
        set {}
    }


    init() {
        super.init(frame: CGRect(x: 0, y: 0, width: 1, height: 1))
        isAccessibilityElement = true
        accessibilityLabel = String(localized: "Keyboard Input for Mac")
        accessibilityTraits = .allowsDirectInteraction
        NotificationCenter.default.addObserver(self, selector: #selector(keyboardWillChangeFrame(_:)), name: UIResponder.keyboardWillChangeFrameNotification, object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(hardwareKeyboardDidDisconnect), name: .GCKeyboardDidDisconnect, object: nil)
    }

    @objc private func keyboardWillChangeFrame(_ notification: Notification) {
        guard isFirstResponder, let screen = window?.windowScene?.screen,
              let frame = notification.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect
        else { return }
        let barHeight = accessoryHost?.view.bounds.height ?? 0
        let isUp = frame.intersection(screen.bounds).height > barHeight + Self.onScreenKeysHeight
        if isUp, !isOnScreenKeyboardUp {
            controller?.setUsesHardwareKeyboard(false)
            updateKeyboardType()
        }
        isOnScreenKeyboardUp = isUp
    }

    @objc private func hardwareKeyboardDidDisconnect() {
        controller?.setUsesHardwareKeyboard(false)
        updateKeyboardType()
    }

    private func updateKeyboardType() {
        let type: UIKeyboardType = controller?.usesHardwareKeyboard == true ? .asciiCapable : .default
        guard type != keyboardType else { return }
        keyboardType = type
        if isFirstResponder { reloadInputViews() }
    }

    required init?(coder: NSCoder) {
        fatalError()
    }

    override var canBecomeFirstResponder: Bool { true }

    override var editingInteractionConfiguration: UIEditingInteractionConfiguration { .none }

    override var inputAccessoryView: UIView? {
        guard let controller else { return nil }
        if let accessoryHost { return accessoryHost.view }
        let host = UIHostingController(rootView: KeyboardAccessoryBar(controller: controller))
        if #available(iOS 16, *) {
            host.sizingOptions = .intrinsicContentSize
        }
        host.view.backgroundColor = .clear
        host.view.autoresizingMask = [.flexibleHeight]
        host.view.translatesAutoresizingMaskIntoConstraints = true
        host.view.frame = CGRect(x: 0, y: 0, width: 320, height: 64)
        accessoryHost = host
        return host.view
    }


    private var document = ""
    private var markedRange: NSRange?
    private var selection = NSRange(location: 0, length: 0)
    private var typedText = ""

    weak var inputDelegate: UITextInputDelegate?
    var markedTextStyle: [NSAttributedString.Key: Any]?

    var tokenizer: UITextInputTokenizer {
        UITextInputStringTokenizer(textInput: self)
    }

    var hasText: Bool { true }

    func insertText(_ text: String) {
        let range = markedRange ?? selection
        document = (document as NSString).replacingCharacters(in: range, with: text)
        markedRange = nil
        selection = NSRange(location: range.location + (text as NSString).length, length: 0)
        documentDidChange()
    }

    func deleteBackward() {
        var deleted = selection
        if deleted.length == 0 {
            guard deleted.location > 0 else {
                if markedRange == nil { deleteBeforeDocument() }
                return
            }
            deleted = (document as NSString).rangeOfComposedCharacterSequence(at: deleted.location - 1)
        }
        if let marked = markedRange {
            deleted = NSIntersectionRange(deleted, marked)
            guard deleted.length > 0 else { return }
            markedRange = marked.length > deleted.length ? NSRange(location: marked.location, length: marked.length - deleted.length) : nil
        }
        document = (document as NSString).replacingCharacters(in: deleted, with: "")
        selection = NSRange(location: deleted.location, length: 0)
        documentDidChange()
    }

    private func deleteBeforeDocument() {
        if controller?.replaceText(deleting: typedText.count + 1, with: typedText) != true {
            dropTypedText()
        }
    }

    func setMarkedText(_ markedText: String?, selectedRange: NSRange) {
        let text = markedText ?? ""
        let range = markedRange ?? selection
        let length = (text as NSString).length
        document = (document as NSString).replacingCharacters(in: range, with: text)
        markedRange = length > 0 ? NSRange(location: range.location, length: length) : nil
        let location = min(max(selectedRange.location, 0), length)
        selection = NSRange(location: range.location + location, length: min(max(selectedRange.length, 0), length - location))
        documentDidChange()
    }

    func unmarkText() {
        guard markedRange != nil else { return }
        markedRange = nil
        documentDidChange()
    }

    func replace(_ range: UITextRange, withText text: String) {
        guard let range = nsRange(range) else { return }
        if markedRange == nil, selection == NSRange(location: NSMaxRange(range), length: 0),
           (document as NSString).substring(with: range) == " ", text == "." || text == "。" {
            return
        }
        let length = (text as NSString).length
        document = (document as NSString).replacingCharacters(in: range, with: text)
        markedRange = nil
        if NSMaxRange(range) <= selection.location {
            selection.location += length - range.length
        } else if range.location < NSMaxRange(selection) {
            selection = NSRange(location: range.location + length, length: 0)
        }
        documentDidChange()
    }

    private func documentDidChange() {
        let text = markedRange.map { (document as NSString).replacingCharacters(in: $0, with: "") } ?? document
        if text != typedText {
            let kept = zip(typedText, text).prefix { $0 == $1 }.count
            let deleting = typedText.count - kept
            typedText = text
            if controller?.replaceText(deleting: deleting, with: String(text.dropFirst(kept))) != true {
                dropTypedText()
            }
        }
    }

    func forgetTypedText() {
        guard !typedText.isEmpty else { return }
        guard markedRange == nil else {
            dropTypedText()
            return
        }
        inputDelegate?.selectionWillChange(self)
        inputDelegate?.textWillChange(self)
        dropTypedText()
        inputDelegate?.textDidChange(self)
        inputDelegate?.selectionDidChange(self)
    }

    private func dropTypedText() {
        typedText = ""
        guard let marked = markedRange else {
            document = ""
            selection = NSRange(location: 0, length: 0)
            return
        }
        document = (document as NSString).substring(with: marked)
        markedRange = NSRange(location: 0, length: marked.length)
        selection = clamped(NSRange(location: selection.location - marked.location, length: selection.length))
    }

    func text(in range: UITextRange) -> String? {
        nsRange(range).map { (document as NSString).substring(with: $0) }
    }

    var markedTextRange: UITextRange? {
        markedRange.map(ComposingTextRange.init)
    }

    var selectedTextRange: UITextRange? {
        get { ComposingTextRange(selection) }
        set { if let range = newValue.flatMap(nsRange) { selection = range } }
    }

    var beginningOfDocument: UITextPosition { ComposingTextPosition(0) }

    var endOfDocument: UITextPosition { ComposingTextPosition(documentLength) }

    func textRange(from fromPosition: UITextPosition, to toPosition: UITextPosition) -> UITextRange? {
        guard let from = fromPosition as? ComposingTextPosition, let to = toPosition as? ComposingTextPosition else { return nil }
        return ComposingTextRange(NSRange(location: min(from.offset, to.offset), length: abs(to.offset - from.offset)))
    }

    func position(from position: UITextPosition, offset: Int) -> UITextPosition? {
        guard let position = position as? ComposingTextPosition else { return nil }
        let offset = position.offset + offset
        return (0...documentLength).contains(offset) ? ComposingTextPosition(offset) : nil
    }

    func position(from position: UITextPosition, in direction: UITextLayoutDirection, offset: Int) -> UITextPosition? {
        switch direction {
        case .left, .up: self.position(from: position, offset: -offset)
        default: self.position(from: position, offset: offset)
        }
    }

    func compare(_ position: UITextPosition, to other: UITextPosition) -> ComparisonResult {
        let offset = offset(from: other, to: position)
        return offset < 0 ? .orderedAscending : offset > 0 ? .orderedDescending : .orderedSame
    }

    func offset(from: UITextPosition, to toPosition: UITextPosition) -> Int {
        guard let from = from as? ComposingTextPosition, let to = toPosition as? ComposingTextPosition else { return 0 }
        return to.offset - from.offset
    }

    func position(within range: UITextRange, farthestIn direction: UITextLayoutDirection) -> UITextPosition? {
        switch direction {
        case .left, .up: range.start
        default: range.end
        }
    }

    func characterRange(byExtending position: UITextPosition, in direction: UITextLayoutDirection) -> UITextRange? {
        switch direction {
        case .left, .up: textRange(from: beginningOfDocument, to: position)
        default: textRange(from: position, to: endOfDocument)
        }
    }

    func baseWritingDirection(for position: UITextPosition, in direction: UITextStorageDirection) -> NSWritingDirection {
        .natural
    }

    func setBaseWritingDirection(_ writingDirection: NSWritingDirection, for range: UITextRange) {}


    func firstRect(for range: UITextRange) -> CGRect {
        caretRect
    }

    func caretRect(for position: UITextPosition) -> CGRect {
        caretRect
    }

    func selectionRects(for range: UITextRange) -> [UITextSelectionRect] {
        []
    }

    func closestPosition(to point: CGPoint) -> UITextPosition? {
        endOfDocument
    }

    func closestPosition(to point: CGPoint, within range: UITextRange) -> UITextPosition? {
        range.end
    }

    func characterRange(at point: CGPoint) -> UITextRange? {
        nil
    }

    private var caretRect: CGRect {
        guard let screen, let controller else { return .zero }
        let point = screen.convert(screen.viewPoint(for: controller.pointerPosition), to: self)
        let height = UIFont.preferredFont(forTextStyle: .body).lineHeight
        return CGRect(x: point.x, y: point.y - height / 2, width: 2, height: height)
    }

    private var documentLength: Int {
        (document as NSString).length
    }

    private func clamped(_ range: NSRange) -> NSRange {
        let location = min(max(range.location, 0), documentLength)
        return NSRange(location: location, length: min(max(range.length, 0), documentLength - location))
    }

    private func nsRange(_ range: UITextRange) -> NSRange? {
        guard let range = range as? ComposingTextRange else { return nil }
        return clamped(range.range)
    }

    override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        let unhandled = screen?.handlePresses(presses, down: true) ?? presses
        updateKeyboardType()
        if !unhandled.isEmpty {
            super.pressesBegan(unhandled, with: event)
        }
    }

    override func pressesEnded(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        let unhandled = screen?.handlePresses(presses, down: false) ?? presses
        if !unhandled.isEmpty {
            super.pressesEnded(unhandled, with: event)
        }
    }

    override func pressesCancelled(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        let unhandled = screen?.handlePresses(presses, down: false) ?? presses
        if !unhandled.isEmpty {
            super.pressesCancelled(unhandled, with: event)
        }
    }


    override func becomeFirstResponder() -> Bool {
        updateKeyboardType()
        let became = super.becomeFirstResponder()
        if became { controller?.keyboardVisibilityDidChange(true) }
        return became
    }

    override func resignFirstResponder() -> Bool {
        let resigned = super.resignFirstResponder()
        if resigned {
            isOnScreenKeyboardUp = false
            dropTypedText()
            controller?.keyboardVisibilityDidChange(false)
        }
        return resigned
    }

    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        screen?.canPerformAction(action, withSender: sender) ?? false
    }

    override func paste(_ sender: Any?) {
        controller?.pasteFromDevice()
    }

    override func target(forAction action: Selector, withSender sender: Any?) -> Any? {
        if action == #selector(paste(_:)) {
            return canPerformAction(action, withSender: sender) ? self : nil
        }
        return controller?.isControlling == true && action != RemoteScreenUIView.mainMenuShortcut ? nil : super.target(forAction: action, withSender: sender)
    }
}

private final class ComposingTextPosition: UITextPosition {
    let offset: Int

    init(_ offset: Int) {
        self.offset = offset
    }
}

private final class ComposingTextRange: UITextRange {
    let range: NSRange

    init(_ range: NSRange) {
        self.range = range
    }

    override var start: UITextPosition { ComposingTextPosition(range.location) }
    override var end: UITextPosition { ComposingTextPosition(NSMaxRange(range)) }
    override var isEmpty: Bool { range.length == 0 }
}
#endif
