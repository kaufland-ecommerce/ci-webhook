package hook_manager

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/hashicorp/go-multierror"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
)

type Manager struct {
	ctx          context.Context
	files        HooksFiles
	logger       *slog.Logger
	asTemplate   bool
	hooksInFiles map[string]Hooks
	watcher      *fsnotify.Watcher
	notifyChan   chan struct{}
	hotReload    bool
}

func NewManager(ctx context.Context, files HooksFiles, asTemplate bool, hotReload bool) *Manager {
	return &Manager{
		ctx:          ctx,
		notifyChan:   make(chan struct{}, 5),
		hooksInFiles: make(map[string]Hooks),
		files:        files,
		logger:       slog.Default(),
		asTemplate:   asTemplate,
		hotReload:    hotReload,
	}
}

func (m *Manager) Start() error {
	var result *multierror.Error
	result = multierror.Append(result, m.Load())
	if m.hotReload {
		result = multierror.Append(result, m.StartFileWatcher())
	}
	if result.ErrorOrNil() != nil {
		m.Close()
	}
	return result.ErrorOrNil()
}

func (m *Manager) reloadWatcher() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.notifyChan:
			m.reloadAllHooks()
		}
	}
}

func (m *Manager) Load() error {
	var result *multierror.Error

	// load and parse hooks
	for _, hooksFilePath := range m.files {
		m.logger.Info("attempting to load hooks", "path", hooksFilePath)
		newHooks := Hooks{}

		err := newHooks.LoadFromFile(hooksFilePath, m.asTemplate)
		if err != nil {
			result = multierror.Append(result, err)
			m.logger.Error("error loading hooks from file", "error", err)
		} else {
			m.logger.Info("loaded hook(s) from file", "path", hooksFilePath, "loaded", len(newHooks))

			for _, h := range newHooks {
				if m.matchLoadedHook(h.ID) != nil {
					// fatal
					result = multierror.Append(result, fmt.Errorf("hook id=%s has already been loaded, check your hooks file for duplicate hooks ids", h.ID))
					m.logger.Error("hook has already been loaded! please check your hooks file for duplicate hooks ids!", "hook_id", h.ID)
					continue
				}
				m.logger.Info("hook loaded", "hook_id", h.ID)
			}
			m.hooksInFiles[hooksFilePath] = newHooks
		}
	}

	newHooksFiles := m.files[:0] // copy?
	for _, filePath := range m.files {
		if _, ok := m.hooksInFiles[filePath]; ok {
			newHooksFiles = append(newHooksFiles, filePath)
		}
	}
	m.files = newHooksFiles
	return result.ErrorOrNil()
}

func (m *Manager) Get(id string) *hook.Hook {
	return m.matchLoadedHook(id)
}

func (m *Manager) matchLoadedHook(id string) *hook.Hook {
	for _, hooks := range m.hooksInFiles {
		if h := hooks.Match(id); h != nil {
			return h
		}
	}
	return nil
}

func (m *Manager) reloadHooks(hooksFilePath string) {
	hooksInFile := Hooks{}
	// parse and swap
	m.logger.Info("attempting to reload hooks from file", "path", hooksFilePath)
	err := hooksInFile.LoadFromFile(hooksFilePath, m.asTemplate)

	if err != nil {
		m.logger.Error("error loading hooks from file", "error", err, "path", hooksFilePath)
	} else {
		seenHooksIds := make(map[string]bool)
		m.logger.Info("found hook(s) in file", "path", hooksFilePath, "loaded", len(hooksInFile))
		for _, h := range hooksInFile {
			wasHookIDAlreadyLoaded := false

			for _, loadedHook := range m.hooksInFiles[hooksFilePath] {
				if loadedHook.ID == h.ID {
					wasHookIDAlreadyLoaded = true
					break
				}
			}

			if (m.matchLoadedHook(h.ID) != nil && !wasHookIDAlreadyLoaded) || seenHooksIds[h.ID] {
				m.logger.Error("hook has already been loaded! please check your hooks file for duplicate hooks ids!", "hook_id", h.ID)
				m.logger.Warn("reverting hooks back to the previous configuration")
				return
			}

			seenHooksIds[h.ID] = true
			m.logger.Info("hook loaded", "hook_id", h.ID)
		}

		m.hooksInFiles[hooksFilePath] = hooksInFile
	}
}

func (m *Manager) reloadAllHooks() {
	for _, hooksFilePath := range m.files {
		m.reloadHooks(hooksFilePath)
	}
}

func (m *Manager) removeHooks(hooksFilePath string) {
	fileSourceToRemove := m.hooksInFiles[hooksFilePath]
	for _, h := range fileSourceToRemove {
		m.logger.Info("removing hook", "hook_id", h.ID)
	}
	// removes fileSourceToRemove from the file list
	newHooksFiles := m.files[:0]
	for _, filePath := range m.files {
		if filePath != hooksFilePath {
			newHooksFiles = append(newHooksFiles, filePath)
		}
	}
	m.files = newHooksFiles

	// removes fileSourceToRemove from the hooksInFiles map
	removedHooksCount := len(fileSourceToRemove)
	delete(m.hooksInFiles, hooksFilePath)
	m.logger.Info("removed hooks", "count", removedHooksCount, "file_source", hooksFilePath)
}

// Notify sends a notification to the manager that the hooks should be reloaded
func (m *Manager) Notify() {
	m.notifyChan <- struct{}{}
}

func (m *Manager) Len() int {
	sum := 0
	for _, hooks := range m.hooksInFiles {
		sum += len(hooks)
	}
	return sum
}

func (m *Manager) Close() {
	if m.watcher != nil {
		_ = m.watcher.Close()
	}
}

func (m *Manager) StartFileWatcher() error {
	var err error
	m.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		m.logger.Error("error creating file watcher instance", "error", err)
		return err
	}
	for _, hooksFilePath := range m.files {
		// set up file watcher
		m.logger.Info("setting up watcher", "file", hooksFilePath)

		err = m.watcher.Add(hooksFilePath)
		if err != nil {
			m.logger.Error("error adding hooks file to the watcher", "error", err, "file", hooksFilePath)
			return err
		}
	}

	go m.watchForFileChange(m.ctx)
	return nil
}

func (m *Manager) watchForFileChange(ctx context.Context) {
	watcher := m.watcher
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-watcher.Events:
			if event.Op&fsnotify.Write == fsnotify.Write {
				m.logger.Info("hooks file modified", "file", event.Name)
				m.reloadHooks(event.Name)
			} else if event.Op&fsnotify.Remove == fsnotify.Remove {
				if _, err := os.Stat(event.Name); os.IsNotExist(err) {
					m.logger.Info("hooks file removed, no longer watching this file for changes, removing hooks that were loaded from it", "file", event.Name)
					_ = watcher.Remove(event.Name)
					m.removeHooks(event.Name)
				}
			} else if event.Op&fsnotify.Rename == fsnotify.Rename {
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(event.Name); os.IsNotExist(err) {
					// the file was removed
					m.logger.Info("hooks file removed, no longer watching this file for changes, removing hooks that were loaded from it", "file", event.Name)
					_ = watcher.Remove(event.Name)
					m.removeHooks(event.Name)
				} else {
					m.logger.Info("hooks file overwritten, reloading hooks", "file", event.Name)
					m.reloadHooks(event.Name)
					_ = watcher.Remove(event.Name)
					_ = watcher.Add(event.Name)
				}
			}
		case err := <-watcher.Errors:
			m.logger.Error("watcher error", "error", err)
		}
	}
}

// HooksFiles is a slice of String
type HooksFiles []string

func (h *HooksFiles) String() string {
	if len(*h) == 0 {
		return "hooks.json"
	}

	return strings.Join(*h, ", ")
}

// Set method appends new string
func (h *HooksFiles) Set(value string) error {
	*h = append(*h, value)
	return nil
}
