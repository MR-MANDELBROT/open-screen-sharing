#if os(macOS)
import SwiftUI

extension View {
    func unboundedFormWidth() -> some View {
        transformEnvironment(\.self) { disableFormContentSizeFitting?(&$0) }
    }
}

// Grouped forms cap their sections at the readable width unless FormContentSizeFittingBehavior is .disabled,
// which only the View.formContentSizeFittingBehavior(_:) SPI can write (macOS 26A434).
private let disableFormContentSizeFitting: ((inout EnvironmentValues) -> Void)? = {
    let handle = UnsafeMutableRawPointer(bitPattern: -2)
    guard let behaviorType = _typeByName("7SwiftUI30FormContentSizeFittingBehaviorV") as? any Hashable.Type,
        let disabled = dlsym(handle, "$s7SwiftUI30FormContentSizeFittingBehaviorV8disabledACvgZ"),
        let modifier = dlsym(handle, "$s7SwiftUI4ViewPAAE30formContentSizeFittingBehavioryQrAA04FormefgH0VF"),
        let modifierResult = dlsym(handle, "$s7SwiftUI4ViewPAAE30formContentSizeFittingBehavioryQrAA04FormefgH0VFQOMQ"),
        let emptyViewConformance = dlsym(handle, "$s7SwiftUI9EmptyViewVAA0D0AAWP"),
        let opaqueTypeMetadata = dlsym(handle, "swift_getOpaqueTypeMetadata2")
    else { return nil }
    func resolve<Behavior: Hashable>(_: Behavior.Type) -> ((inout EnvironmentValues) -> Void)? {
        let spi = unsafeBitCast(
            (disabled, UnsafeRawPointer?.none, modifier, UnsafeRawPointer?.none, opaqueTypeMetadata, UnsafeRawPointer?.none),
            to: FittingSPI<Behavior>.self)
        let emptyView = [unsafeBitCast(EmptyView.self as Any.Type, to: UnsafeRawPointer.self), UnsafeRawPointer(emptyViewConformance)]
        let resultType = emptyView.withUnsafeBufferPointer { spi.opaqueTypeMetadata(0, $0.baseAddress!, modifierResult, 0).0 }
        guard resultType == ModifiedContent<EmptyView, _EnvironmentKeyWritingModifier<Behavior>>.self else { return nil }
        var behavior = spi.disabled()
        let writer = spi.modifier(&behavior, EmptyView.self, emptyViewConformance).modifier
        return { $0[keyPath: writer.keyPath] = writer.value }
    }
    return resolve(behaviorType)
}()

nonisolated private struct FittingSPI<Behavior> {
    let disabled: () -> Behavior
    let modifier: (UnsafePointer<Behavior>, Any.Type, UnsafeRawPointer) -> ModifiedContent<EmptyView, _EnvironmentKeyWritingModifier<Behavior>>
    let opaqueTypeMetadata: (Int, UnsafePointer<UnsafeRawPointer>, UnsafeRawPointer, Int) -> (Any.Type, Int)
}
#endif
