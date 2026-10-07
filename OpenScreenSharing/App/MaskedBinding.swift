import SwiftUI

extension Binding<String> {
    func masked(revealed: @escaping () -> Bool) -> Binding<String> {
        Binding {
            revealed() ? wrappedValue : String(repeating: "•", count: wrappedValue.count)
        } set: {
            if revealed() {
                wrappedValue = $0
            }
        }
    }
}
