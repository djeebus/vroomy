package plugins

import (
	"fmt"
	"path/filepath"
	"plugin"
	"reflect"
	"sync"

	"github.com/Hatch1fy/errors"
	"github.com/missionMeteora/journaler"
)

const (
	// ErrExpectedEndParen is returned when an ending parenthesis is missing
	ErrExpectedEndParen = errors.Error("expected ending parenthesis")
	// ErrInvalidDir is returned when a directory is empty
	ErrInvalidDir = errors.Error("invalid directory, cannot be empty")
	// ErrPluginKeyExists is returned when a plugin cannot be added because it already exists
	ErrPluginKeyExists = errors.Error("plugin cannot be added, key already exists")
	// ErrPluginNotLoaded is returned when a plugin namespace is provided that has not been loaded
	ErrPluginNotLoaded = errors.Error("plugin with that key has not been loaded")
	// ErrNotAddressable is returned when a non-addressable value is provided
	ErrNotAddressable = errors.Error("provided backend must be addressable")
)

// New will return a new instance of plugins
func New(dir string) (pp *Plugins, err error) {
	if len(dir) == 0 {
		err = ErrInvalidDir
		return
	}

	var p Plugins
	p.out = journaler.New("Plugins")
	p.dir = dir
	p.m = make(map[string]*Plugin)
	pp = &p
	return
}

// Plugins manages loaded plugins
type Plugins struct {
	mu  sync.RWMutex
	out *journaler.Journaler

	// Root directory
	dir string

	// Internal plugin store (by key)
	m map[string]*Plugin

	closed bool
}

func (p *Plugins) getPlugin(key string) (alias, filename string, err error) {
	if key, alias = parseKey(key); err != nil {
		return
	}

	switch {
	case filepath.Ext(key) != "":
		if len(alias) == 0 {
			alias = getPluginKey(key)
		}

		filename = key
		return

	case isGitReference(key):
		if len(alias) == 0 {
			if alias, err = getGitPluginKey(key); err != nil {
				return
			}
		}

		// Set filename
		filename = filepath.Join(p.dir, alias+".so")

		// Check to see if current plugin exists
		if doesPluginExist(filename) {
			return
		}

		err = p.gitRetrieve(key, filename)

	default:
		fmt.Println("Not supported?")
	}

	return
}

func (p *Plugins) gitRetrieve(gitURL, filename string) (err error) {
	p.out.Notification("About to get: %v", gitURL)
	if err = goGet(gitURL, false); err != nil {
		return
	}

	p.out.Success("Download of %s complete", gitURL)

	p.out.Notification("About to build: %v", gitURL)
	if err = goBuild(gitURL, filename); err != nil {
		return
	}

	p.out.Success("Build of %s complete", gitURL)
	return
}

// New will load a new plugin by plugin key
// The following formats are accepted as keys:
//	- path/to/file/plugin.so
//	- github.com/username/repository/pluginDir
func (p *Plugins) New(pluginKey string) (key string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var pi Plugin
	if pi.alias, pi.filename, err = p.getPlugin(pluginKey); err != nil {
		return
	}

	if _, ok := p.m[pi.alias]; ok {
		err = ErrPluginKeyExists
		return
	}

	p.m[pi.alias] = &pi
	p.out.Success("%s (%s) loaded", pi.alias, pluginKey)
	key = pi.alias
	return
}

// Initialize will initialize all loaded plugins
func (p *Plugins) Initialize() (err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.out.Notification("Initializing plugins")

	for _, pi := range p.m {
		if err = pi.init(); err != nil {
			return
		}
	}

	return
}

// Get will return a plugin by key
func (p *Plugins) Get(key string) (plugin *plugin.Plugin, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var (
		pi *Plugin
		ok bool
	)

	if pi, ok = p.m[key]; !ok {
		err = fmt.Errorf("Cannot find plugin %s: %v", key, ErrPluginNotLoaded)
		return
	}

	plugin = pi.p
	return
}

// Backend will associated the backend of the requested key
func (p *Plugins) Backend(key string, backend interface{}) (err error) {
	var pi *plugin.Plugin
	if pi, err = p.Get(key); err != nil {
		return
	}

	var sym plugin.Symbol
	if sym, err = pi.Lookup("Backend"); err != nil {
		return
	}

	fn, ok := sym.(func() interface{})
	if !ok {
		return fmt.Errorf("invalid symbol, expected func() interface{} and received %v", reflect.TypeOf(sym))
	}

	refVal := reflect.ValueOf(backend)
	elem := refVal.Elem()
	if !elem.CanSet() {
		return ErrNotAddressable
	}

	beVal := reflect.ValueOf(fn())

	if elem.Type() != beVal.Type() {
		return fmt.Errorf("invalid type, expected %v and received %v", elem.Type(), beVal.Type())
	}

	elem.Set(beVal)
	return
}

// Close will close plugins
func (p *Plugins) Close() (err error) {
	p.mu.Lock()
	p.mu.Unlock()
	if p.closed {
		return errors.ErrIsClosed
	}

	var errs errors.ErrorList
	p.out.Notification("Closing plugins")
	for key, pi := range p.m {
		if err = closePlugin(pi.p); err != nil {
			errs.Push(fmt.Errorf("error closing %s: %v", key, err))
			continue
		}

		p.out.Success("Closed %s", key)
	}

	p.closed = true
	return errs.Err()
}

type backendFn func() interface{}
