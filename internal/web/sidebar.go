package web

import (
	"context"
	"html/template"
	"sort"

	"leanfeed/internal/store"
)

// NavItem is a sidebar link with its unread count.
type NavItem struct {
	Name    string
	Href    template.URL
	Unread  int
	Current bool
}

type viewItem struct {
	NavItem
	Key string
}

type feedItem struct {
	NavItem
	ID    string
	Error string // last fetch error, if the last fetch failed
}

type folderItem struct {
	NavItem
	Feeds []feedItem
}

type sidebarData struct {
	Views   []viewItem
	Folders []folderItem
	Feeds   []feedItem // not in a folder
	OOB     bool
}

// sidebarData lists views, folders and feeds with unread counts. Folder
// and feed links keep the current view; current is also highlighted.
func (s *Server) sidebarData(ctx context.Context, current listQuery) (sidebarData, error) {
	feeds, err := s.store.ListFeeds(ctx)
	if err != nil {
		return sidebarData{}, err
	}
	counts, err := s.store.UnreadCounts(ctx)
	if err != nil {
		return sidebarData{}, err
	}

	var d sidebarData
	total := 0
	folders := map[string]*folderItem{}
	for _, f := range feeds {
		q := listQuery{View: current.View, Feed: f.ID, Page: 1}
		item := feedItem{
			NavItem: NavItem{Name: f.Title, Href: q.URL(), Unread: counts[f.ID], Current: current.Feed == f.ID},
			ID:      f.ID,
		}
		if f.LastStatus == store.StatusError {
			item.Error = f.LastError
		}
		total += item.Unread
		if f.Folder == "" {
			d.Feeds = append(d.Feeds, item)
			continue
		}
		folder := folders[f.Folder]
		if folder == nil {
			q := listQuery{View: current.View, Folder: f.Folder, Page: 1}
			folder = &folderItem{NavItem: NavItem{Name: f.Folder, Href: q.URL(), Current: current.Folder == f.Folder && current.Feed == ""}}
			folders[f.Folder] = folder
		}
		folder.Unread += item.Unread
		folder.Feeds = append(folder.Feeds, item)
	}
	for _, f := range folders {
		d.Folders = append(d.Folders, *f)
	}
	sort.Slice(d.Folders, func(i, j int) bool { return d.Folders[i].Name < d.Folders[j].Name })

	for _, key := range []string{"unread", "all", "starred"} {
		q := listQuery{View: key, Page: 1}
		item := viewItem{NavItem: NavItem{
			Name: viewNames[key], Href: q.URL(),
			Current: current.View == key && current.Feed == "" && current.Folder == "",
		}, Key: key}
		if key == "unread" {
			item.Unread = total
		}
		d.Views = append(d.Views, item)
	}
	return d, nil
}
