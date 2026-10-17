import ObjectiveC
import UIKit

enum BadgeDots {
    private static let itemForRequest = NSSelectorFromString("tabBarController:sidebar:itemForRequest:")
    private static var extendedClasses: Set<ObjectIdentifier> = []

    static func install() {
        colorTabBarBadges()
        if #available(iOS 18, *) {
            extendSidebarDelegates()
        }
    }

    private static func colorTabBarBadges() {
        let selector = #selector(setter: UITabBarItem.badgeValue)
        guard let method = class_getInstanceMethod(UITabBarItem.self, selector) else { return }
        typealias Setter = @convention(c) (UITabBarItem, Selector, NSString?) -> Void
        let original = unsafeBitCast(method_getImplementation(method), to: Setter.self)
        let setter: @convention(block) (UITabBarItem, NSString?) -> Void = { item, value in
            item.badgeColor = value?.length == 0 ? .systemGreen : nil
            original(item, selector, value)
        }
        method_setImplementation(method, imp_implementationWithBlock(setter))
    }

    @available(iOS 18, *)
    private static func extendSidebarDelegates() {
        let selector = #selector(setter: UITabBarController.Sidebar.delegate)
        guard let method = class_getInstanceMethod(UITabBarController.Sidebar.self, selector) else { return }
        typealias Setter = @convention(c) (UITabBarController.Sidebar, Selector, AnyObject?) -> Void
        let original = unsafeBitCast(method_getImplementation(method), to: Setter.self)
        let setter: @convention(block) (UITabBarController.Sidebar, AnyObject?) -> Void = { sidebar, delegate in
            if let delegate {
                extend(object_getClass(delegate))
            }
            original(sidebar, selector, delegate)
        }
        method_setImplementation(method, imp_implementationWithBlock(setter))
    }

    @available(iOS 18, *)
    private static func extend(_ owner: AnyClass?) {
        guard let owner, extendedClasses.insert(ObjectIdentifier(owner)).inserted else { return }
        typealias MakeItem = @convention(c) (
            AnyObject, Selector, UITabBarController, UITabBarController.Sidebar, UITabSidebarItem.Request
        ) -> UITabSidebarItem
        let original = class_getInstanceMethod(owner, itemForRequest).map {
            unsafeBitCast(method_getImplementation($0), to: MakeItem.self)
        }
        let makeItem: @convention(block) (
            AnyObject, UITabBarController, UITabBarController.Sidebar, UITabSidebarItem.Request
        ) -> UITabSidebarItem = { delegate, controller, sidebar, request in
            let item = original?(delegate, itemForRequest, controller, sidebar, request) ?? UITabSidebarItem(request: request)
            addDot(to: item)
            return item
        }
        let implementation = imp_implementationWithBlock(makeItem)
        if !class_addMethod(owner, itemForRequest, implementation, "@@:@@@"),
            let method = class_getInstanceMethod(owner, itemForRequest)
        {
            method_setImplementation(method, implementation)
        }
    }

    @available(iOS 18, *)
    private static func addDot(to item: UITabSidebarItem) {
        guard case .tab(let tab) = item.content, tab.badgeValue?.isEmpty == true else { return }
        let configuration = UICellAccessory.CustomViewConfiguration(
            customView: DotView(), placement: .trailing(), reservedLayoutWidth: .actual, maintainsFixedSize: true)
        item.accessories.append(.customView(configuration: configuration))
    }
}

private final class DotView: UIView {
    init() {
        let size = UIFontMetrics(forTextStyle: .body).scaledValue(for: 8)
        super.init(frame: CGRect(x: 0, y: 0, width: size, height: size))
        backgroundColor = .systemGreen
        layer.cornerRadius = size / 2
    }

    required init?(coder: NSCoder) {
        fatalError()
    }

    override var intrinsicContentSize: CGSize {
        bounds.size
    }
}
