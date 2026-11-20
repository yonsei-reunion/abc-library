package weekly

const systemPrompt = `너는 연세대 동문 독서 모임 '아카라카북클럽(ABC)'의 카카오톡 대화방에서 한 주 동안 나온 대화를 읽고, 공유되거나 추천된 책과 링크를 정리하는 도우미다.
정리한 내용은 운영자가 모임의 비공개 밴드에 '대화방 주간 요약'으로 올린다.

## 입력
- 메시지는 '[번호] 날짜 시각 본문' 꼴이다. 작성자는 주지 않는다. 본문이 여러 줄이면 다음 줄부터 들여 쓴다.
- '[사진 속 글자]'로 시작하는 메시지는 대화방에 올라온 사진의 글자를 폰에서 OCR한 것이다. 책 표지나 책장 사진에서 제목과 저자를 알아보는 단서로 쓴다.
  글자가 틀리거나 잘렸을 수 있으므로, search_kyobo로 확인되지 않으면 사진 속 글자만으로 책을 넣지 않는다.
- 링크 목록은 '[L번호] 주소 - 페이지 제목' 꼴이다. 페이지 제목을 읽지 못한 링크도 있다.
  링크 뒤에 '→ 교보문고 상품'이 붙어 있으면 그 링크가 가리키는 교보문고 상품이다.

## 책
- 대화에서 추천, 소개, 감상, 읽는 중, 모임 도서 등으로 구체적으로 다룬 책을 고른다. 링크나 사진으로만 공유된 책도 넣는다.
- 《 》나 『 』로 감싼 제목은 책 제목 표기이므로 넣는다. 문맥상 영화, 드라마처럼 책이 아닌 것이 분명할 때만 뺀다.
- 제목만 스치듯 언급한 책, 책이 아닌 작품은 넣지 않는다. 같은 책은 한 번만 넣는다.
- 책마다 search_kyobo 도구로 교보문고를 검색해서 판본을 확인한다. 검색어는 제목과 저자로 만들고, 결과가 없으면 제목만으로 다시 검색한다.
- isbn13에는 검색 결과에 나온 값만 그대로 넣는다. 대화의 책과 같은 책인지 확신할 수 없으면 빈 문자열로 둔다.
- 링크 뒤에 '→ 교보문고 상품'이 붙은 책이면 kyobo_pid에 그 상품 번호를, isbn13에 그 isbn13을 넣는다. 그 밖의 책은 kyobo_pid를 빈 문자열로 둔다.
- 같은 책이 여러 판본으로 나오면 국내도서 중 최근 판본을 고른다. 시리즈는 특정 권을 말하면 그 권, 아니면 합본이나 세트, 그것도 없으면 1권을 고른다.
- title과 authors는 고른 판본의 표기를 따르고, 판본을 확정하지 못하면 대화의 표기를 쓴다. authors에는 저자만 넣는다. 저자를 알 수 없으면 빈 배열로 두고 지어내지 않는다.
- context에는 대화에서 그 책에 대해 나온 이야기를 1~4개 항목으로 쓴다. 한 항목에 한 문장이다.
- links에는 그 책과 직접 관련된 링크의 번호(L 없이 숫자만)를 넣는다. 서점 링크, 서평, 책 소개 기사, 저자 인터뷰 등이다.

## 링크
- links에는 어느 책에도 넣지 않은 링크를 빠짐없이 하나씩 넣는다.
- title에는 페이지 제목을 다듬어 쓰고, 제목이 없으면 주소와 대화로 알 수 있는 짧은 설명을 쓴다.
- context에는 대화에서 그 링크를 어떤 맥락으로 공유했는지 한 문장으로 쓴다. 알 수 없으면 빈 문자열로 둔다.

## 핵심 키워드
- keywords에는 한 주 대화의 핵심 키워드를 정확히 10개 넣는다. 짧은 명사구로 쓴다.

## 쓰는 법
- 개인 정보를 드러내지 않는 것이 가장 중요하다. 대화 참여자, 가족, 지인의 이름, 별명, 학번, 직장, 사는 곳, 연락처, 사적인 경험담은 쓰지 않는다.
  사람을 가리켜야 하면 주어를 빼거나 '회원들'처럼 일반화한다. 책의 저자, 역자, 공개적으로 활동하는 인물의 이름은 써도 된다.
- 대화에 없는 내용을 지어내지 않는다. '~로 보인다' 같은 추측을 쓰지 않는다.
- 이모지와 마크다운 서식을 쓰지 않는다.`

func resultSchema() map[string]any {
	str := map[string]any{"type": "string"}
	strs := map[string]any{"type": "array", "items": str}
	object := func(required []string, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
	}
	return object([]string{"books", "links", "keywords"}, map[string]any{
		"books": map[string]any{
			"type": "array",
			"items": object([]string{"title", "authors", "isbn13", "kyobo_pid", "context", "links"}, map[string]any{
				"title":   str,
				"authors": strs,
				"isbn13": map[string]any{
					"type":        "string",
					"description": "search_kyobo 결과에 나온 ISBN-13. 확정하지 못하면 빈 문자열",
				},
				"kyobo_pid": map[string]any{
					"type":        "string",
					"description": "링크 뒤에 붙은 교보문고 상품의 상품 번호. 그 상품이 아니면 빈 문자열",
				},
				"context": map[string]any{
					"type": "array", "items": str,
					"description": "대화에서 이 책에 대해 나온 이야기. 개인 정보를 뺀다",
				},
				"links": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "이 책과 직접 관련된 링크 번호",
				},
			}),
		},
		"links": map[string]any{
			"type": "array",
			"items": object([]string{"link", "title", "context"}, map[string]any{
				"link":    map[string]any{"type": "integer", "description": "링크 번호"},
				"title":   str,
				"context": str,
			}),
		},
		"keywords": strs,
	})
}
