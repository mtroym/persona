import CoreGraphics
import Testing
@testable import Persona

/// Full Page's layout (#1609): where each line goes, the strips drawn at a time, and the rect the screen showed, in the
/// PDF's space (origin at the bottom left).
struct FullPageLayoutTests {
    @Test func linesAreSpacedAndInsetLikeTheList() {
        let l = FullPageLayout(heights: [100, 50, 200], spacing: 16, inset: 16, stripHeight: 1_000)
        #expect(l.tops == [16, 132, 198])
        let total: CGFloat = 16 + 100 + 16 + 50 + 16 + 200 + 16
        #expect(l.height == total)
        #expect(l.strips == [.init(lines: [0, 1, 2], y: 16, height: 382)])
        #expect(FullPageLayout(heights: [], spacing: 16, inset: 16, stripHeight: 1_000).height == 0)
    }

    @Test func stripsStayUnderTheirHeightAndALongLineIsOneOfItsOwn() {
        let l = FullPageLayout(heights: [300, 300, 300, 2_000, 100], spacing: 10, inset: 0, stripHeight: 700)
        #expect(l.strips.map(\.lines) == [[0, 1], [2], [3], [4]])
        #expect(l.strips.map(\.y) == [0, 620, 930, 2_940])
        #expect(l.strips[0].height == 610 && l.strips[3].height == 100)
        // Every line drawn once, in order, and each strip starts where its first line does.
        #expect(l.strips.flatMap(\.lines) == Array(0..<5))
        #expect(l.strips.allSatisfy { $0.y == l.tops[$0.lines[0]] })
    }

    @Test func theVisibleRectIsInPDFSpace() {
        let l = FullPageLayout(heights: [400, 400, 400], spacing: 0, inset: 0, stripHeight: 2_000)
        // At the newest end: the page's last viewport, at the bottom of PDF space.
        #expect(l.visibleRect(anchor: nil, viewport: 600, width: 390) == CGRect(x: 0, y: 0, width: 390, height: 600))
        // The second line on top: its top 400 pt down the page is 1200 - 400 - 600 = 200 up in PDF space.
        #expect(l.visibleRect(anchor: 1, viewport: 600, width: 390) == CGRect(x: 0, y: 200, width: 390, height: 600))
        // The first line: the page's top.
        #expect(l.visibleRect(anchor: 0, viewport: 600, width: 390) == CGRect(x: 0, y: 600, width: 390, height: 600))
        // The last line: no lower than the page's bottom.
        #expect(l.visibleRect(anchor: 2, viewport: 600, width: 390) == CGRect(x: 0, y: 0, width: 390, height: 600))
        // A viewport taller than the page is the page.
        #expect(l.visibleRect(anchor: nil, viewport: 5_000, width: 390) == CGRect(x: 0, y: 0, width: 390, height: 1_200))
        // An anchor the page does not have: the newest end.
        #expect(l.visibleRect(anchor: 9, viewport: 600, width: 390) == CGRect(x: 0, y: 0, width: 390, height: 600))
    }
}
