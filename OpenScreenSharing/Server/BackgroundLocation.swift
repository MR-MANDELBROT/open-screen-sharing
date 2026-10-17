import CoreLocation

final class BackgroundLocation: NSObject {
    private(set) var authorization: CLAuthorizationStatus

    private(set) var isUpdating = false {
        didSet {
            guard isUpdating != oldValue else { return }
            onUpdatingChange?()
        }
    }

    var onUpdatingChange: (() -> Void)?
    var onDenied: (() -> Void)?

    private let manager = CLLocationManager()
    private var isWanted = false

    override init() {
        authorization = manager.authorizationStatus
        super.init()
        manager.desiredAccuracy = kCLLocationAccuracyThreeKilometers
        manager.pausesLocationUpdatesAutomatically = false
        manager.delegate = self
    }

    var isDenied: Bool {
        authorization == .denied || authorization == .restricted
    }

    func start() {
        guard !isWanted else { return }
        isWanted = true
        switch authorization {
        case .notDetermined:
            manager.requestWhenInUseAuthorization()
        case .authorizedWhenInUse, .authorizedAlways:
            startUpdating()
        default:
            onDenied?()
        }
    }

    func stop() {
        isWanted = false
        stopUpdating()
    }

    private func startUpdating() {
        guard isWanted, !isUpdating else { return }
        manager.allowsBackgroundLocationUpdates = true
        manager.startUpdatingLocation()
        isUpdating = true
    }

    private func stopUpdating() {
        guard isUpdating else { return }
        manager.stopUpdatingLocation()
        manager.allowsBackgroundLocationUpdates = false
        isUpdating = false
    }
}

extension BackgroundLocation: CLLocationManagerDelegate {
    func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        authorization = manager.authorizationStatus
        switch authorization {
        case .authorizedWhenInUse, .authorizedAlways:
            startUpdating()
        default:
            stopUpdating()
            if isWanted, isDenied {
                onDenied?()
            }
        }
    }

    func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {}

    func locationManager(_ manager: CLLocationManager, didFailWithError error: any Error) {}
}
