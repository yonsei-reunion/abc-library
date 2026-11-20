package band

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// 밴드에 쓰는 동작은 이 파일에만 둔다. 대화방 주간 요약 글을 일주일에 한 번 올리는 것 말고는 쓰지 않는다.
// 글쓰기 설정(알림, 공지 등록 등)은 건드리지 않고 기본값으로 올린다. 게시판을 쓰는 밴드에서는 정해진 게시판만 고른다.

const (
	writeLayer  = "section._lyWrap"
	writeEditor = writeLayer + " div._richEditor[contenteditable=true]"
	// 편집기는 글 속 주소마다 링크 미리보기 카드를 붙인다. 손으로 올린 글처럼 카드를 남긴다.
	writeSnippet = writeEditor + ` .cke_widget_wrapper[data-type="snippet"]`
	writeSubmit  = writeLayer + " button._btnSubmitPost"
	openWriteBtn = "button._btnOpenWriteLayer"

	// boardPrompt는 게시판 선택 창의 안내 문구다. 게시판을 쓰는 밴드는 게시 버튼을 누르면 이 창을 띄우고,
	// 게시판을 고르지 않으면 글을 올리지 않는다.
	boardPrompt = "게시판을 선택해야 글이 등록됩니다"
	// boardClickTimeout은 게시판 선택 창의 항목과 확인이 나타나기를 기다리는 시간이다.
	boardClickTimeout = 5 * time.Second

	// snippetPoll은 링크 미리보기 카드 수를 세는 간격이고, snippetSettle은 카드 수가 이만큼 그대로면 다 붙은 것으로 본다.
	// 미리보기를 주지 않는 사이트는 카드가 붙지 않으므로 주소 수만큼 기다리지 않는다(2026-11 실측: 주소 23개에 카드 17개,
	// 빠진 카드는 순서와 상관없이 위키백과, 알라딘, medium 같은 사이트였다).
	snippetPoll   = 2 * time.Second
	snippetSettle = 3
	// snippetTimeout은 카드가 붙기를 기다리는 최대 시간이다. 넘으면 붙은 카드만으로 게시한다.
	snippetTimeout = 90 * time.Second

	// writeCloseTimeout은 게시 버튼을 누른 뒤 글쓰기 창이 닫히기를 기다리는 시간이다.
	writeCloseTimeout = 90 * time.Second
	// maxWriteStates는 게시가 막혔을 때 오류에 남기는 화면 상태의 수다.
	maxWriteStates = 10
)

// writeStateScript는 게시 버튼을 누른 뒤의 화면을 읽는다. 게시가 막힌 이유를 알려고 쓴다.
// 글자는 편집기와 게시글 카드 밖의 안내와 쓰기 창을 덮은 층에서만 읽는다. 편집기는 글자 수만 센다.
const writeStateScript = `({layer, editor, submit}) => {
  const clean = t => (t || '').trim().replace(/\s+/g, ' ').slice(0, 200);
  const shown = e => { const r = e.getBoundingClientRect(), s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const content = 'article, [contenteditable=true]';
  const ed = document.querySelector(editor);
  const btn = document.querySelector(submit);
  const box = ed && ed.closest(layer);
  const notes = [...document.querySelectorAll(
    '[role=alert], [role=alertdialog], [role=dialog], [class*=toast i], [class*=alert i], [class*=error i], [class*=popup i], [class*=modal i]')]
    .filter(e => shown(e) && !e.contains(ed) && !e.closest(content))
    .map(e => clean(e.innerText)).filter(Boolean);
  // 쓰기 창 위를 덮은 층은 클래스 이름과 상관없이 찾는다. 화면 가운데에 있는 요소가 쓰기 창 밖이면 덮인 것이다.
  const top = document.elementFromPoint(innerWidth / 2, innerHeight / 2);
  let cover = '';
  if (top && box && !box.contains(top) && !top.closest(content)) {
    cover = (top.className || top.tagName).toString().slice(0, 60);
    for (let e = top, i = 0; e && i < 4 && !e.contains(ed) && !e.querySelector('article'); e = e.parentElement, i++) {
      const t = clean(e.innerText);
      if (t) { notes.push(t); break; }
    }
  }
  return JSON.stringify({
    submit: btn ? {disabled: btn.disabled, aria: btn.getAttribute('aria-disabled') || '', class: btn.className.toString().slice(0, 80)} : null,
    chars: ed ? ed.textContent.length : 0,
    paragraphs: ed ? ed.querySelectorAll(':scope > p').length : 0,
    cover,
    notes: [...new Set(notes)].slice(0, 5),
  });
}`

// clickTextScript는 보이는 글자가 text와 똑같은 요소 중 맨 뒤의 것을 페이지에서 직접 누르고, 눌렀는지 돌려준다.
// 편집기와 게시글 카드 속 글자는 누르지 않는다.
const clickTextScript = `text => {
  const clean = e => (e.innerText || '').trim().replace(/\s+/g, ' ');
  const shown = e => { const r = e.getBoundingClientRect(), s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const hit = [...document.querySelectorAll('body *')]
    .filter(e => !e.closest('article, [contenteditable=true], script, style') && shown(e) && clean(e) === text
      && ![...e.children].some(k => clean(k) === text)).at(-1);
  if (!hit) return false;
  hit.click();
  return true;
}`

// boardLayerScript는 게시판 선택 창의 구조를 읽는다. 창을 고치지 못했을 때 오류에 남긴다.
// 편집기와 게시글 카드를 품지 않는 범위까지만 올라가고, 태그와 일부 속성과 게시판 이름만 남긴다.
const boardLayerScript = `prompt => {
  const ed = document.querySelector('[contenteditable=true]');
  let e = [...document.querySelectorAll('body *')].filter(x => (x.textContent || '').includes(prompt)).at(-1);
  if (!e || e.closest('article, [contenteditable=true]')) return '';
  const safe = p => p && p !== document.body && !p.contains(ed) && !p.querySelector('article');
  while (safe(e.parentElement)) e = e.parentElement;
  const copy = e.cloneNode(true);
  copy.querySelectorAll('img, svg, [contenteditable]').forEach(x => x.remove());
  [copy, ...copy.querySelectorAll('*')].forEach(x => {
    if (x.checked) x.setAttribute('checked', '');
    [...x.attributes].forEach(a => {
      if (!['class', 'type', 'role', 'checked', 'disabled'].includes(a.name)) x.removeAttribute(a.name); });
  });
  return copy.outerHTML.replace(/\s+/g, ' ').slice(0, 4000);
}`

// recentTextsScript는 게시글 목록 첫 화면에 그려진 글의 본문만 읽는다. 작성자 영역은 읽지 않는다.
const recentTextsScript = `() => JSON.stringify([...document.querySelectorAll('article._postMainWrap')]
  .map(card => { const t = card.querySelector('.postBody .txtBody'); return t ? t.innerText : ''; })
  .filter(t => t))`

// RecentTexts는 게시글 목록 첫 화면에 나온 최근 글들의 본문을 읽는다. 같은 글을 두 번 올리지 않으려고 확인할 때 쓴다.
func (c *Crawler) RecentTexts() ([]string, error) {
	if err := c.openPostList(c.page.Locator("article._postMainWrap").First(), "게시글 목록을 읽지 못했습니다"); err != nil {
		return nil, err
	}
	texts, err := evalJSON[[]string](c.page, recentTextsScript)
	if err != nil {
		return nil, fmt.Errorf("게시글 목록을 읽지 못했습니다: %w", err)
	}
	return texts, nil
}

// Posted는 최근 글 중에 marker가 든 글이 있는지 알려 준다.
func (c *Crawler) Posted(marker string) (bool, error) {
	texts, err := c.RecentTexts()
	if err != nil {
		return false, err
	}
	return ContainsMarker(texts, marker), nil
}

// ContainsMarker는 texts 중에 marker가 든 글이 있는지 알려 준다. 밴드는 줄마다 문단을 만들어 빈 줄을 늘리므로 공백을 무시하고 비교한다.
func ContainsMarker(texts []string, marker string) bool {
	m := strings.Join(strings.Fields(marker), " ")
	if m == "" {
		return false
	}
	for _, t := range texts {
		if strings.Contains(strings.Join(strings.Fields(t), " "), m) {
			return true
		}
	}
	return false
}

// Write는 content를 밴드에 새 글로 올린다. 글쓰기 창에 넣은 내용이 content와 같은지 확인한 뒤 게시한다.
// 올린 뒤 게시글 목록에서 marker가 든 글을 찾아 올라갔는지 확인한다.
// 오류 메시지에는 글 내용을 넣지 않는다.
func (c *Crawler) Write(content, marker string) error {
	got, err := c.fillEditor(content)
	if err != nil {
		return err
	}
	if !SameLines(got, content) {
		return fmt.Errorf("글쓰기 편집기에 넣은 내용이 요약과 달라서 올리지 않았습니다(%s)", lineDiff(got, content))
	}
	c.takeDialogs()
	if err := c.page.Locator(writeSubmit).First().Click(playwright.LocatorClickOptions{Timeout: ms(writeClickTimeout)}); err != nil {
		return fmt.Errorf("게시 버튼을 누르지 못했습니다: %w", err)
	}
	if err := c.waitWriteClosed(writeCloseTimeout); err != nil {
		return err
	}
	// 글쓰기 창이 닫힌 뒤 목록에 새 글이 그려지기를 기다린다.
	time.Sleep(3 * time.Second)
	posted, err := c.Posted(marker)
	if err != nil {
		return fmt.Errorf("게시했지만 목록에서 확인하지 못했습니다: %w", err)
	}
	if !posted {
		return errors.New("게시했지만 목록에서 새 글을 찾지 못했습니다")
	}
	return nil
}

// waitWriteClosed는 게시 버튼을 누른 뒤 글쓰기 창이 닫히기를 기다린다. 잠깐 떴다 사라지는 안내를 놓치지 않으려고
// 1초마다 화면을 읽고, 끝내 닫히지 않으면 본 상태와 알림창 문구를 오류에 담는다.
// Playwright 오류의 호출 기록에는 요소의 내용이 섞일 수 있으므로 오류에 넣지 않는다.
func (c *Crawler) waitWriteClosed(timeout time.Duration) error {
	editor := c.page.Locator(writeEditor).First()
	var states []string
	chosen := false
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Second) {
		// 게시한 뒤 화면이 바뀌는 중이면 읽기가 실패할 수 있으므로 오류를 실패로 치지 않는다.
		if visible, err := editor.IsVisible(); err == nil && !visible {
			return nil
		}
		s := c.writeState()
		if s != "" && len(states) < maxWriteStates && !slices.Contains(states, s) {
			states = append(states, s)
		}
		// 게시 버튼을 되풀이해 누르면 글이 두 번 올라갈 수 있으므로 게시판은 한 번만 고른다.
		if !chosen {
			var err error
			if chosen, err = c.chooseBoard(); err != nil {
				return err
			}
			if chosen {
				states = append(states, "게시판 고름: "+c.Board)
			}
		}
	}
	if l := c.boardLayer(); l != "" {
		states = append(states, l)
	}
	return fmt.Errorf("게시한 뒤 글쓰기 창이 닫히지 않았습니다(%s)", describeWrite(c.takeDialogs(), states))
}

// chooseBoard는 게시판 선택 창이 떠 있으면 c.Board를 고르고 확인을 누른 뒤 게시 버튼을 다시 누른다. 창이 없으면 false를 돌려준다.
// 게시판을 쓰지 않는 밴드에는 창이 뜨지 않는다. Playwright의 클릭은 게시판 항목에 닿지 않아서(2026-11 실측) 페이지에서 직접 누른다.
func (c *Crawler) chooseBoard() (bool, error) {
	if visible, err := c.page.GetByText(boardPrompt).First().IsVisible(); err != nil || !visible {
		return false, nil
	}
	if c.Board == "" {
		return false, errors.New("게시판 선택 창이 떴지만 고를 게시판이 정해지지 않았습니다")
	}
	// 화면의 게시판 이름은 #으로 시작한다.
	if !c.clickText("#" + c.Board) {
		return false, fmt.Errorf("게시판 선택 창에서 #%s 항목을 누르지 못했습니다(%s)", c.Board, c.describeBoard())
	}
	time.Sleep(500 * time.Millisecond)
	if !c.clickText("확인") {
		return false, fmt.Errorf("게시판 선택 창의 확인을 누르지 못했습니다(%s)", c.describeBoard())
	}
	// 확인을 누르면 밴드는 창을 닫고 글쓰기 창으로 돌아갈 뿐 글을 올리지 않는다(2026-11 실측: 90초 동안 그대로이고 본문만 7자, 2문단 늘었다).
	// 첫 클릭은 이 창에 막혀서 올라가지 않았으므로 여기서 한 번 더 눌러도 글은 하나만 올라간다.
	time.Sleep(500 * time.Millisecond)
	if err := c.page.Locator(writeSubmit).First().Click(playwright.LocatorClickOptions{Timeout: ms(boardClickTimeout)}); err != nil {
		return false, fmt.Errorf("게시판을 고른 뒤 게시 버튼을 누르지 못했습니다(%s)", c.describeBoard())
	}
	return true, nil
}

// clickText는 글자가 text인 요소가 나타나기를 기다려서 누른다.
func (c *Crawler) clickText(text string) bool {
	for deadline := time.Now().Add(boardClickTimeout); ; time.Sleep(200 * time.Millisecond) {
		v, err := c.page.Evaluate(clickTextScript, text)
		if clicked, _ := v.(bool); err == nil && clicked {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// boardLayer는 게시판 선택 창의 구조를 읽는다. 창이 없으면 빈 문자열이다.
func (c *Crawler) boardLayer() string {
	return evalString(c.page, boardLayerScript, boardPrompt)
}

// describeBoard는 게시판을 고르지 못했을 때 오류에 넣을 설명을 만든다.
func (c *Crawler) describeBoard() string {
	return describeWrite(c.takeDialogs(), []string{c.writeState(), c.boardLayer()})
}

// writeState는 지금 화면의 상태를 JSON으로 읽는다. 읽지 못하면 빈 문자열이다.
func (c *Crawler) writeState() string {
	return evalString(c.page, writeStateScript, map[string]any{"layer": writeLayer, "editor": writeEditor, "submit": writeSubmit})
}

// describeWrite는 글쓰기 창이 닫히지 않았을 때 오류에 넣을 설명을 만든다.
func describeWrite(dialogs, states []string) string {
	var parts []string
	if len(dialogs) > 0 {
		parts = append(parts, "알림창 "+strings.Join(dialogs, " / "))
	}
	if len(states) > 0 {
		parts = append(parts, "화면 "+strings.Join(states, " / "))
	}
	if len(parts) == 0 {
		return "화면 상태를 읽지 못함"
	}
	return strings.Join(parts, ", ")
}

// fillEditor는 글쓰기 창을 열어 content를 넣고, 편집기에 들어간 글을 줄 단위로 읽어 돌려준다. 게시하지는 않는다.
func (c *Crawler) fillEditor(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", errors.New("올릴 글이 비어 있습니다")
	}
	opener := c.page.Locator(openWriteBtn).First()
	if err := c.openPostList(opener, "글쓰기 버튼을 찾지 못했습니다"); err != nil {
		return "", err
	}
	click := playwright.LocatorClickOptions{Timeout: ms(writeClickTimeout)}
	if err := opener.Click(click); err != nil {
		return "", fmt.Errorf("글쓰기 창을 열지 못했습니다: %w", err)
	}
	editor := c.page.Locator(writeEditor).First()
	if err := editor.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: ms(editorWaitTimeout)}); err != nil {
		return "", fmt.Errorf("글쓰기 편집기를 찾지 못했습니다: %w", err)
	}
	if err := editor.Click(click); err != nil {
		return "", err
	}
	// 줄마다 입력하면 편집기가 주소의 미리보기 카드를 만드는 동안 입력을 잃는다(2026-10 실측: 231줄 중 수십 줄).
	// 붙여넣기 이벤트로 한 번에 넣으면 줄마다 문단이 되고 빠지는 줄이 없다.
	if _, err := editor.Evaluate(`(e, text) => { const dt = new DataTransfer(); dt.setData('text/plain', text);
	  e.dispatchEvent(new ClipboardEvent('paste', {clipboardData: dt, bubbles: true, cancelable: true})); }`, strings.TrimSpace(content)); err != nil {
		return "", fmt.Errorf("글쓰기 편집기에 글을 넣지 못했습니다: %w", err)
	}
	c.waitSnippets(strings.Count(content, "http://") + strings.Count(content, "https://"))
	// 미리보기 카드는 문단 사이의 div로 붙으므로 문단만 읽는다. 빈 편집기의 안내 문구 문단은 글이 아니므로 뺀다.
	typed, err := editor.Evaluate(`e => [...e.querySelectorAll(':scope > p')].filter(p => !p.querySelector('._placeholder'))
	  .map(p => p.innerText.replace(/\n$/, '')).join('\n')`, nil)
	if err != nil {
		return "", err
	}
	got, _ := typed.(string)
	return got, nil
}

// waitSnippets는 편집기가 링크 미리보기 카드를 붙이기를 기다린다. 카드는 주소마다 조금씩 늦게 붙으므로
// 카드 수가 주소 수(urls)에 이르거나 한동안 늘지 않으면 끝낸다. 카드는 없어도 글은 올라가므로 오류로 치지 않는다.
func (c *Crawler) waitSnippets(urls int) int {
	if urls == 0 {
		return 0
	}
	last, same := -1, 0
	for deadline := time.Now().Add(snippetTimeout); time.Now().Before(deadline); {
		time.Sleep(snippetPoll)
		n, err := c.page.Locator(writeSnippet).Count()
		if err != nil {
			continue
		}
		if n >= urls {
			return n
		}
		if n == last {
			if same++; same >= snippetSettle {
				return n
			}
		} else {
			last, same = n, 0
		}
	}
	return last
}

// lineDiff는 처음 달라지는 줄 번호와 양쪽 줄 수를 알려 준다. 글 내용은 넣지 않는다.
func lineDiff(got, content string) string {
	g, w := comparableLines(got), comparableLines(content)
	n := 0
	for n < len(g) && n < len(w) && g[n] == w[n] {
		n++
	}
	return fmt.Sprintf("%d번째 줄부터 다름, 편집기 %d줄, 요약 %d줄", n+1, len(g), len(w))
}

// SameLines는 편집기에서 읽은 글이 content와 같은지 줄 단위로 비교한다. 줄 끝 공백은 무시한다.
func SameLines(got, content string) bool {
	return slices.Equal(comparableLines(got), comparableLines(content))
}

// comparableLines는 SameLines와 lineDiff가 비교하는 줄이다. 편집기가 넣는 줄바꿈 없는 공백(U+00A0)은 공백으로 보고,
// 글 앞뒤의 공백과 줄 끝 공백은 뺀다.
func comparableLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\u00a0", " ")), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return lines
}
