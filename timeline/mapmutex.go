/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

import "sync"

// Modified from https://medium.com/@petrlozhkin/kmutex-lock-mutex-by-unique-id-408467659c24

type mapMutex struct {
	cond *sync.Cond
	set  map[any]struct{}
}

func newMapMutex() *mapMutex {
	return &mapMutex{
		cond: sync.NewCond(new(sync.Mutex)),
		set:  make(map[any]struct{}),
	}
}

func (mmu *mapMutex) Lock(key any) {
	mmu.cond.L.Lock()
	defer mmu.cond.L.Unlock()
	for mmu.locked(key) {
		mmu.cond.Wait()
	}
	mmu.set[key] = struct{}{}
}

func (mmu *mapMutex) Unlock(key any) {
	mmu.cond.L.Lock()
	defer mmu.cond.L.Unlock()
	delete(mmu.set, key)
	mmu.cond.Broadcast()
}

func (mmu *mapMutex) locked(key any) (ok bool) {
	_, ok = mmu.set[key]
	return
}
