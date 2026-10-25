package chronos

import "iter"

const iterateSize = 100

type identified interface {
	resourceID() string
}

func iterate[T identified](list func(page int) (*Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		seen := make(map[string]bool)
		for page := 1; ; page++ {
			p, err := list(page)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range p.Data {
				id := item.resourceID()
				if seen[id] {
					continue
				}
				seen[id] = true
				if !yield(item, nil) {
					return
				}
			}
			if len(p.Data) == 0 || page >= p.Paging.Pages {
				return
			}
		}
	}
}

func iteratePageSize(size int) int {
	if size == 0 {
		return iterateSize
	}
	return size
}
