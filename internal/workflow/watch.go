package workflow

import (
	"context"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

func WatchFile(ctx context.Context, path string, onChange func(), onError func(error)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	if err := watcher.Add(directory); err != nil {
		_ = watcher.Close()
		return err
	}
	go func() {
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-watcher.Errors:
				if err != nil && onError != nil {
					onError(err)
				}
			case event := <-watcher.Events:
				if filepath.Base(event.Name) != base {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Chmod) {
					if onChange != nil {
						onChange()
					}
				}
			}
		}
	}()
	return nil
}