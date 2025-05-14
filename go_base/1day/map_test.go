package test

import "unsafe"

type hmap struct {
	count     int    // 当前元素个数
	flags     uint8  // 状态标志
	B         uint8  // 桶数量的对数(可容纳 2^B 个桶)
	noverflow uint16 // 溢出桶的大约数量
	hash0     uint32 // 哈希种子

	buckets    unsafe.Pointer // 2^B 个桶的数组
	oldbuckets unsafe.Pointer // 扩容时保存旧桶
	nevacuate  uintptr        // 迁移进度计数器

	extra *mapextra // 可选字段
}

type bmap struct {
	tophash [bucketCnt]uint8 // 每个键的哈希值高8位
	// 后面跟着 bucketCnt 个键和 bucketCnt 个值
	// 最后可能有一个溢出指针
}

func makemap(t *maptype, hint int, h *hmap) *hmap {
	mem, overflow := math.MulUintptr(uintptr(hint), t.Bucket.Size_)
	if overflow || mem > maxAlloc {
		hint = 0
	}

	// initialize Hmap
	if h == nil {
		h = new(hmap)
	}
	h.hash0 = uint32(rand())

	// Find the size parameter B which will hold the requested # of elements.
	// For hint < 0 overLoadFactor returns false since hint < bucketCnt.
	B := uint8(0)
	for overLoadFactor(hint, B) {
		B++
	}
	h.B = B

	// allocate initial hash table
	// if B == 0, the buckets field is allocated lazily later (in mapassign) 如果B为0 懒加载
	// If hint is large zeroing this memory could take a while.
	if h.B != 0 {
		var nextOverflow *bmap
		h.buckets, nextOverflow = makeBucketArray(t, h.B, nil)
		if nextOverflow != nil {
			h.extra = new(mapextra)
			h.extra.nextOverflow = nextOverflow
		}
	}

	return h
}

/*
1、计算内存
计算预估需要的内存大小 = hint * 每个桶的大小
检查是否溢出或超过最大可分配内存
如果超出限制，将 hint 重置为 0

2、初始化 hmap 结构
如果传入的 hmap 为 nil，则新建一个
初始化哈希种子 hash0（用于哈希计算）

3. 确定桶的数量 (B 值)
B 表示桶数量的对数（实际桶数 = 2^B）
通过循环找到满足 hint 要求的最小 B 值
overLoadFactor 检查是否超过负载因子（6.5）

4. 分配桶数组
如果 B > 0，分配桶数组
makeBucketArray 创建桶数组并可能预分配一些溢出桶
如果有预分配的溢出桶，初始化 extra 字段



*********关键点解析
1、惰性初始化：
当 B == 0 时，桶数组不会立即分配
实际桶数组会在第一次写入时分配（在 mapassign 中）

2、负载因子：
默认负载因子为 6.5（每个桶平均元素数）
计算公式：loadFactor = hint / 2^B
当 loadFactor > 6.5 时增加 B 值

3、内存优化：
对于小的 hint 值，使用最小的桶数量
避免为小的 map 预分配过多内存

4、哈希种子：
每个 map 实例有独立的 hash0
防止哈希碰撞攻击


*/

func mapassign(t *maptype, h *hmap, key unsafe.Pointer) unsafe.Pointer {
	if h == nil {
		panic(plainError("assignment to entry in nil map"))
	}
	if raceenabled {
		callerpc := getcallerpc()
		pc := abi.FuncPCABIInternal(mapassign)
		racewritepc(unsafe.Pointer(h), callerpc, pc)
		raceReadObjectPC(t.Key, key, callerpc, pc)
	}
	if msanenabled { // 调试时 帮助发现程序中未初始化内存读取的问题
		msanread(key, t.Key.Size_)
	}
	if asanenabled { // 启用 AddressSanitizer (ASan) 检查，用于检测内存访问错误（如越界读取、释放后使用等）。
		asanread(key, t.Key.Size_)
	}
	if h.flags&hashWriting != 0 {
		fatal("concurrent map writes")
	}
	hash := t.Hasher(key, uintptr(h.hash0))

	// Set hashWriting after calling t.hasher, since t.hasher may panic,
	// in which case we have not actually done a write.
	h.flags ^= hashWriting

	if h.buckets == nil {
		h.buckets = newobject(t.Bucket) // newarray(t.Bucket, 1)
	}

again:
	bucket := hash & bucketMask(h.B)
	if h.growing() {
		growWork(t, h, bucket)
	}
	b := (*bmap)(add(h.buckets, bucket*uintptr(t.BucketSize)))
	top := tophash(hash)

	var inserti *uint8
	var insertk unsafe.Pointer
	var elem unsafe.Pointer
bucketloop:
	for {
		for i := uintptr(0); i < abi.MapBucketCount; i++ {
			if b.tophash[i] != top {
				if isEmpty(b.tophash[i]) && inserti == nil {
					inserti = &b.tophash[i]
					insertk = add(unsafe.Pointer(b), dataOffset+i*uintptr(t.KeySize))
					elem = add(unsafe.Pointer(b), dataOffset+abi.MapBucketCount*uintptr(t.KeySize)+i*uintptr(t.ValueSize))
				}
				if b.tophash[i] == emptyRest {
					break bucketloop
				}
				continue
			}
			k := add(unsafe.Pointer(b), dataOffset+i*uintptr(t.KeySize))
			if t.IndirectKey() {
				k = *((*unsafe.Pointer)(k))
			}
			if !t.Key.Equal(key, k) {
				continue
			}
			// already have a mapping for key. Update it.
			if t.NeedKeyUpdate() {
				typedmemmove(t.Key, k, key)
			}
			elem = add(unsafe.Pointer(b), dataOffset+abi.MapBucketCount*uintptr(t.KeySize)+i*uintptr(t.ValueSize))
			goto done
		}
		ovf := b.overflow(t)
		if ovf == nil {
			break
		}
		b = ovf
	}

	// Did not find mapping for key. Allocate new cell & add entry.

	// If we hit the max load factor or we have too many overflow buckets,
	// and we're not already in the middle of growing, start growing.
	if !h.growing() && (overLoadFactor(h.count+1, h.B) || tooManyOverflowBuckets(h.noverflow, h.B)) {
		hashGrow(t, h)
		goto again // Growing the table invalidates everything, so try again
	}

	if inserti == nil {
		// The current bucket and all the overflow buckets connected to it are full, allocate a new one.
		newb := h.newoverflow(t, b)
		inserti = &newb.tophash[0]
		insertk = add(unsafe.Pointer(newb), dataOffset)
		elem = add(insertk, abi.MapBucketCount*uintptr(t.KeySize))
	}

	// store new key/elem at insert position
	if t.IndirectKey() {
		kmem := newobject(t.Key)
		*(*unsafe.Pointer)(insertk) = kmem
		insertk = kmem
	}
	if t.IndirectElem() {
		vmem := newobject(t.Elem)
		*(*unsafe.Pointer)(elem) = vmem
	}
	typedmemmove(t.Key, insertk, key)
	*inserti = top
	h.count++

done:
	if h.flags&hashWriting == 0 {
		fatal("concurrent map writes")
	}
	h.flags &^= hashWriting
	if t.IndirectElem() {
		elem = *((*unsafe.Pointer)(elem))
	}
	return elem
}
