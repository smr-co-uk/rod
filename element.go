package rod

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/go-rod/rod/lib/assets/js"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/rod/lib/utils"
)

// Element represents the DOM element
type Element struct {
	ctx     context.Context
	sleeper func() utils.Sleeper

	page *Page

	Object *proto.RuntimeRemoteObject
}

// Focus sets focus on the specified element
func (el *Element) Focus() error {
	err := el.ScrollIntoView()
	if err != nil {
		return err
	}

	_, err = el.Evaluate(NewEval(`this.focus()`).ByUser())
	return err
}

// ScrollIntoView scrolls the current element into the visible area of the browser
// window if it's not already within the visible area.
func (el *Element) ScrollIntoView() error {
	defer el.tryTraceInput("scroll into view")()
	el.page.browser.trySlowmotion()

	return proto.DOMScrollIntoViewIfNeeded{ObjectID: el.id()}.Call(el)
}

// Hover the mouse over the center of the element.
func (el *Element) Hover() error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	err = el.ScrollIntoView()
	if err != nil {
		return err
	}

	pt, err := el.Interactable()
	if err != nil {
		return err
	}

	err = el.page.Mouse.Move(pt.X, pt.Y, 1)
	if err != nil {
		return err
	}

	return nil
}

// Click will press then release the button just like a human.
func (el *Element) Click(button proto.InputMouseButton) error {
	err := el.Hover()
	if err != nil {
		return err
	}

	defer el.tryTraceInput(string(button) + " click")()

	return el.page.Mouse.Click(button)
}

// Tap the button just like a human.
func (el *Element) Tap() error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	err = el.ScrollIntoView()
	if err != nil {
		return err
	}

	pt, err := el.Interactable()
	if err != nil {
		return err
	}

	defer el.tryTraceInput("tap")()

	return el.page.Touch.Tap(pt.X, pt.Y)
}

// Interactable checks if the element is interactable with cursor.
// The cursor can be mouse, finger, stylus, etc.
// If not interactable err will be ErrNotInteractable, such as when covered by a modal,
func (el *Element) Interactable() (pt *proto.Point, err error) {
	shape, err := el.Shape()
	if err != nil {
		return
	}

	pt = shape.OnePointInside()
	if pt == nil {
		err = newErr(ErrNotInteractable, el, "element has no visible shape")
		return
	}

	scroll, err := el.page.Root().Eval(`{ x: window.scrollX, y: window.scrollY }`)
	if err != nil {
		return
	}

	elAtPoint, err := el.page.ElementFromPoint(
		int64(pt.X)+scroll.Value.Get("x").Int(),
		int64(pt.Y)+scroll.Value.Get("y").Int(),
	)
	if err != nil {
		return
	}

	yes, err := el.ContainsElement(elAtPoint)
	if err != nil {
		return
	}

	if !yes {
		err = newErr(ErrNotInteractable, elAtPoint, "another element covers current one")
	}
	return
}

// Shape of the DOM element content. The shape is a group of 4-sides polygons (4-gons).
// A 4-gon is not necessary a rectangle. 4-gons can be apart from each other.
// For example, we use 2 4-gons to describe the shape below:
//
//     ┌────────┐   ┌────────┐
//     │    ┌───┘ = └────────┘ + ┌────┐
//     └────┘                    └────┘
//
func (el *Element) Shape() (*proto.DOMGetContentQuadsResult, error) {
	return proto.DOMGetContentQuads{ObjectID: el.id()}.Call(el)
}

// Press a key
func (el *Element) Press(key rune) error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	err = el.Focus()
	if err != nil {
		return err
	}

	defer el.tryTraceInput("press " + input.Keys[key].Key)()

	return el.page.Keyboard.Press(key)
}

// SelectText selects the text that matches the regular expression
func (el *Element) SelectText(regex string) error {
	err := el.Focus()
	if err != nil {
		return err
	}

	defer el.tryTraceInput("select text: " + regex)()
	el.page.browser.trySlowmotion()

	_, err = el.Evaluate(jsHelper(js.SelectText, regex).ByUser())
	return err
}

// SelectAllText selects all text
func (el *Element) SelectAllText() error {
	err := el.Focus()
	if err != nil {
		return err
	}

	defer el.tryTraceInput("select all text")()
	el.page.browser.trySlowmotion()

	_, err = el.Evaluate(jsHelper(js.SelectAllText).ByUser())
	return err
}

// Input focus the element and input text to it.
// To empty the input you can use something like el.SelectAllText().MustInput("")
func (el *Element) Input(text string) error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	err = el.Focus()
	if err != nil {
		return err
	}

	defer el.tryTraceInput("input " + text)()

	err = el.page.Keyboard.InsertText(text)
	if err != nil {
		return err
	}

	_, err = el.Evaluate(jsHelper(js.InputEvent).ByUser())
	return err
}

// Blur is similar to the method Blur
func (el *Element) Blur() error {
	_, err := el.Evaluate(NewEval("this.blur()").ByUser())
	return err
}

// Select the children option elements that match the selectors, the selector can be text content or css selector
func (el *Element) Select(selectors []string) error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	defer el.tryTraceInput(fmt.Sprintf(`select "%s"`, strings.Join(selectors, "; ")))()
	el.page.browser.trySlowmotion()

	_, err = el.Evaluate(jsHelper(js.Select, selectors).ByUser())
	return err
}

// Matches checks if the element can be selected by the css selector
func (el *Element) Matches(selector string) (bool, error) {
	res, err := el.Eval(`s => this.matches(s)`, selector)
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

// Attribute is similar to the method Attribute
func (el *Element) Attribute(name string) (*string, error) {
	attr, err := el.Eval("(n) => this.getAttribute(n)", name)
	if err != nil {
		return nil, err
	}

	if attr.Value.Type == gjson.Null {
		return nil, nil
	}

	return &attr.Value.Str, nil
}

// Property is similar to the method Property
func (el *Element) Property(name string) (proto.JSON, error) {
	prop, err := el.Eval("(n) => this[n]", name)
	if err != nil {
		return proto.JSON{}, err
	}

	return prop.Value, nil
}

// SetFiles of the current file input element
func (el *Element) SetFiles(paths []string) error {
	absPaths := []string{}
	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		utils.E(err)
		absPaths = append(absPaths, absPath)
	}

	defer el.tryTraceInput(fmt.Sprintf("set files: %v", absPaths))()
	el.page.browser.trySlowmotion()

	err := proto.DOMSetFileInputFiles{
		Files:    absPaths,
		ObjectID: el.id(),
	}.Call(el)

	return err
}

// Describe the current element
func (el *Element) Describe(depth int, pierce bool) (*proto.DOMNode, error) {
	val, err := proto.DOMDescribeNode{ObjectID: el.id(), Depth: int64(depth), Pierce: pierce}.Call(el)
	if err != nil {
		return nil, err
	}
	return val.Node, nil
}

// NodeID of the node
func (el *Element) NodeID() (proto.DOMNodeID, error) {
	el.page.enableNodeQuery()
	node, err := proto.DOMRequestNode{ObjectID: el.id()}.Call(el)
	if err != nil {
		return 0, err
	}
	return node.NodeID, nil
}

// ShadowRoot returns the shadow root of this element
func (el *Element) ShadowRoot() (*Element, error) {
	node, err := el.Describe(1, false)
	if err != nil {
		return nil, err
	}

	// though now it's an array, w3c changed the spec of it to be a single.
	id := node.ShadowRoots[0].BackendNodeID

	shadowNode, err := proto.DOMResolveNode{BackendNodeID: id}.Call(el)
	if err != nil {
		return nil, err
	}

	return el.page.ElementFromObject(shadowNode.Object), nil
}

// Frame creates a page instance that represents the iframe
func (el *Element) Frame() (*Page, error) {
	node, err := el.Describe(1, false)
	if err != nil {
		return nil, err
	}

	newPage := *el.page
	newPage.FrameID = node.FrameID
	newPage.element = el
	newPage.jsHelperObj = nil
	newPage.windowObj = nil
	return &newPage, nil
}

// ContainsElement check if the target is equal or inside the element.
func (el *Element) ContainsElement(target *Element) (bool, error) {
	res, err := el.Evaluate(jsHelper(js.ContainsElement, target.Object))
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

// Text that the element displays
func (el *Element) Text() (string, error) {
	str, err := el.Evaluate(jsHelper(js.Text))
	if err != nil {
		return "", err
	}
	return str.Value.String(), nil
}

// HTML of the element
func (el *Element) HTML() (string, error) {
	str, err := el.Eval(`this.outerHTML`)
	if err != nil {
		return "", err
	}
	return str.Value.String(), nil
}

// Visible returns true if the element is visible on the page
func (el *Element) Visible() (bool, error) {
	res, err := el.Evaluate(jsHelper(js.Visible))
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

// WaitLoad for element like <img>
func (el *Element) WaitLoad() error {
	_, err := el.Evaluate(jsHelper(js.WaitLoad))
	return err
}

// WaitStable not using requestAnimation here because it can trigger to many checks,
// or miss checks for jQuery css animation.
func (el *Element) WaitStable(interval time.Duration) error {
	err := el.WaitVisible()
	if err != nil {
		return err
	}

	shape, err := el.Shape()
	if err != nil {
		return err
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
		case <-el.ctx.Done():
			return el.ctx.Err()
		}
		current, err := el.Shape()
		if err != nil {
			return err
		}
		if reflect.DeepEqual(shape, current) {
			break
		}
		shape = current
	}
	return nil
}

// Wait until the js returns true
func (el *Element) Wait(opts *Eval) error {
	return utils.Retry(el.ctx, el.sleeper(), func() (bool, error) {
		res, err := el.Evaluate(opts.This(el.Object))
		if err != nil {
			return true, err
		}

		if res.Value.Bool() {
			return true, nil
		}

		return false, nil
	})
}

// WaitVisible until the element is visible
func (el *Element) WaitVisible() error {
	return el.Wait(jsHelper(js.Visible))
}

// WaitInvisible until the element invisible
func (el *Element) WaitInvisible() error {
	return el.Wait(jsHelper(js.Invisible))
}

// CanvasToImage get image data of a canvas.
// The default format is image/png.
// The default quality is 0.92.
// doc: https://developer.mozilla.org/en-US/docs/Web/API/HTMLCanvasElement/toDataURL
func (el *Element) CanvasToImage(format string, quality float64) ([]byte, error) {
	res, err := el.Eval(`(format, quality) => this.toDataURL(format, quality)`, format, quality)
	if err != nil {
		return nil, err
	}

	_, bin := parseDataURI(res.Value.Str)
	return bin, nil
}

// Resource returns the "src" content of current element. Such as the jpg of <img src="a.jpg">
func (el *Element) Resource() ([]byte, error) {
	src, err := el.Evaluate(jsHelper(js.Resource))
	if err != nil {
		return nil, err
	}

	res, err := proto.PageGetResourceContent{
		FrameID: el.page.FrameID,
		URL:     src.Value.String(),
	}.Call(el)
	if err != nil {
		return nil, err
	}

	data := res.Content

	var bin []byte
	if res.Base64Encoded {
		bin, err = base64.StdEncoding.DecodeString(data)
		utils.E(err)
	} else {
		bin = []byte(data)
	}

	return bin, nil
}

// Screenshot of the area of the element
func (el *Element) Screenshot(format proto.PageCaptureScreenshotFormat, quality int) ([]byte, error) {
	err := el.WaitVisible()
	if err != nil {
		return nil, err
	}

	err = el.ScrollIntoView()
	if err != nil {
		return nil, err
	}

	// so that it won't clip the css-transformed element
	box, err := el.Evaluate(jsHelper(js.Rect))
	if err != nil {
		return nil, err
	}

	opts := &proto.PageCaptureScreenshot{
		Format: format,
		Clip: &proto.PageViewport{
			X:      box.Value.Get("x").Num,
			Y:      box.Value.Get("y").Num,
			Width:  box.Value.Get("width").Num,
			Height: box.Value.Get("height").Num,
			Scale:  1,
		},
	}

	return el.page.Root().Screenshot(false, opts)
}

// Release is a shortcut for Page.Release(el.Object)
func (el *Element) Release() error {
	return el.page.Context(el.ctx).Release(el.Object)
}

// Remove the element from the page
func (el *Element) Remove() error {
	_, err := el.Eval(`this.remove()`)
	if err != nil {
		return err
	}
	return el.Release()
}

// CallContext parameters for proto
func (el *Element) CallContext() (context.Context, proto.Client, string) {
	return el.ctx, el.page.browser, string(el.page.SessionID)
}

// Eval js on the page. For more info check the Element.Evaluate
func (el *Element) Eval(js string, params ...interface{}) (*proto.RuntimeRemoteObject, error) {
	return el.Evaluate(NewEval(js, params...))
}

// Evaluate is just a shortcut of Page.Evaluate with This set to current element.
func (el *Element) Evaluate(opts *Eval) (*proto.RuntimeRemoteObject, error) {
	return el.page.Context(el.ctx).Evaluate(opts.This(el.Object))
}

func (el *Element) id() proto.RuntimeRemoteObjectID {
	return el.Object.ObjectID
}

func (el *Element) ensureParentPage(nodeID proto.DOMNodeID, obj *proto.RuntimeRemoteObject) error {
	has, err := el.page.hasElement(obj)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	// DFS for the iframe that holds the element
	var walk func(page *Page) error
	walk = func(page *Page) error {
		list, err := page.Elements("iframe")
		if err != nil {
			return err
		}

		for _, f := range list {
			p, err := f.Frame()
			if err != nil {
				return err
			}

			obj, err := p.resolveNode(nodeID)
			if err != nil {
				return err
			}
			if obj.ObjectID != "" {
				el.page = p
				el.Object = obj
				return io.EOF
			}

			err = walk(p)
			if err != nil {
				return err
			}
		}
		return nil
	}

	err = walk(el.page)
	if err == io.EOF {
		return nil
	}
	return err
}
