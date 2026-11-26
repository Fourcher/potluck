import XCTest

/// Walks through every screen against a running Potluck and saves screenshots.
/// Needs a fresh pairing link:
///   TEST_RUNNER_POTLUCK_LINK='potluck://pair?…' TEST_RUNNER_POTLUCK_SHOTS=/some/folder xcodebuild test …
/// Without a link the test is skipped.
final class ScreensTests: XCTestCase {
    let env = ProcessInfo.processInfo.environment
    var app: XCUIApplication!

    func shot(_ name: String) {
        let png = XCUIScreen.main.screenshot().pngRepresentation
        let a = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["POTLUCK_SHOTS"] {
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    func field(_ label: String) -> XCUIElement {
        let tv = app.textViews[label]
        return tv.exists ? tv : app.textFields[label]
    }

    func testScreens() throws {
        guard let link = env["POTLUCK_LINK"], !link.isEmpty else { throw XCTSkip("No pairing link given") }
        app = XCUIApplication()
        app.launch()

        // Pair by pasting the link.
        let linkField = field("Pairing link")
        XCTAssertTrue(linkField.waitForExistence(timeout: 10))
        shot("1-pair")
        linkField.tap()
        linkField.typeText(link)
        app.buttons["Pair"].tap()

        let menu = app.buttons["Chats and settings"]
        XCTAssertTrue(menu.waitForExistence(timeout: 30), "chat screen did not appear after pairing")
        sleep(4)
        shot("2-chat-empty")

        // Ask something and wait for the answer to finish.
        let msg = field("Message")
        msg.tap()
        msg.typeText("Name three fruits in one short line.")
        app.buttons["Send"].tap()
        XCTAssertTrue(app.buttons["Stop"].waitForExistence(timeout: 10), "no Stop button while answering")
        sleep(2)
        shot("3-answering")
        let done = NSPredicate(format: "exists == false")
        expectation(for: done, evaluatedWith: app.buttons["Stop"])
        waitForExpectations(timeout: 180)
        sleep(1)
        shot("4-answer")

        // Models sheet.
        app.buttons["modelStatus"].tap()
        sleep(3)
        shot("5-models")
        app.buttons["Done"].tap()

        // Chats and settings sheet.
        menu.tap()
        sleep(2)
        shot("6-chats")
        app.swipeUp()
        sleep(1)
        shot("7-settings")
        app.buttons["Done"].firstMatch.tap()

        // Tasks: the list, then the newest task.
        app.buttons["Tasks"].tap()
        sleep(3)
        shot("8-tasks")
        let first = app.cells.firstMatch
        if first.waitForExistence(timeout: 5) {
            first.tap()
            sleep(3)
            shot("9-task")
        }
    }
}
