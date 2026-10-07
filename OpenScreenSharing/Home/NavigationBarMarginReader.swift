import SwiftUI
import UIKit

struct NavigationBarMarginReader: UIViewRepresentable {
    @Binding var margin: CGFloat

    func makeUIView(context: Context) -> ReaderView {
        ReaderView()
    }

    func updateUIView(_ view: ReaderView, context: Context) {
        view.onChange = { value in
            if margin != value {
                margin = value
            }
        }
        view.report()
    }

    final class ReaderView: UIView {
        var onChange: ((CGFloat) -> Void)?

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) {
            fatalError()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            report()
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            report()
        }

        func report() {
            let controller = sequence(first: self as UIResponder, next: \.next).lazy.compactMap { $0 as? UIViewController }.first
            guard let bar = controller?.navigationController?.navigationBar else { return }
            let safeArea = bar.effectiveUserInterfaceLayoutDirection == .rightToLeft ? bar.safeAreaInsets.right : bar.safeAreaInsets.left
            let value = bar.directionalLayoutMargins.leading - safeArea
            DispatchQueue.main.async { [weak self] in
                self?.onChange?(value)
            }
        }
    }
}
