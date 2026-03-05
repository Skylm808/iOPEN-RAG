# Go 并发：Goroutine、Channel 与 Sync 锁

Go 最核心的并发哲学是：**“不要通过共享内存来通信，而应通过通信来共享内存。”**

## 1. 三者定位与关系
*   **Goroutine**：并发执行体（“干活的工人”）。
*   **Channel**：通信管道（“传送带”）。以**转移数据所有权**的方式避免竞争。
*   **Sync 锁**：状态保护（“排队机制”）。通过**共享内存加锁**的方式防止数据被写坏。

**选择标准**：需要**控制数据流转、任务分发**用 Channel；只需**保护共享变量（如计数器、缓存字典）**用 Sync 锁。

---

## 2. Channel 底层详解

虽然 Channel 在使用时给人“无锁通信”的优雅感，但在 Go runtime 源码中，它的核心结构 `hchan` 实际上是一个**基于锁机制的并发安全队列**。

### 核心数据结构 (`runtime.hchan`)
*   **环形队列（数据缓冲）**：
    *   `buf`：指向底层数据数组的指针（仅有缓冲 Channel 拥有）。
    *   `qcount` / `dataqsiz`：当前队列中元素的数量 / 环形队列的容量。
    *   `sendx` / `recvx`：记录发送和接收在环形数组中的索引位置。
    *   **注意**：无缓冲和有缓冲 Channel 用的是同一个 `hchan` 结构体，这些字段都存在。区别在于无缓冲时 `buf` 指向 nil（不分配数组）、`dataqsiz` 为 0、`sendx`/`recvx` 无意义——整组缓冲字段形同虚设。无缓冲时真正干活的是下面的 `sendq`、`recvq` 和 `lock`。
*   **等待队列（双向链表）**：
    *   `sendq`：**发送等待队列**——可以理解为"发货排队区"。当一个 Goroutine 执行 `ch <- val` 但数据塞不进去时（缓冲满了，或者根本没缓冲），这个 Goroutine 就会被"冻结"：runtime 把它连同要发送的数据一起打包成一个 `sudog` 结构体，挂到 `sendq` 链表上，然后让出 CPU 去睡觉。直到有人来 `<-ch` 取数据，才会把它唤醒。
    *   `recvq`：**接收等待队列**——可以理解为"取货排队区"。当一个 Goroutine 执行 `val := <-ch` 但没有数据可取时（缓冲为空，或者根本没缓冲），同样被冻结打包成 `sudog` 挂到 `recvq` 上睡觉。直到有人往 Channel 发数据，才会被唤醒并拿到数据。
    *   **一句话总结**：`sendq` 和 `recvq` 就是两个"休息室"，分别存放着"想发但发不出去"和"想收但收不到"的 Goroutine。
*   **并发保护**：
    *   `lock`（互斥锁）：用于保护整个 `hchan` 结构。任何对 Channel 的读写操作，都必须先获取此锁。

### 有缓冲 Channel vs 无缓冲 Channel

| | 无缓冲 `make(chan int)` | 有缓冲 `make(chan int, 5)` |
| :--- | :--- | :--- |
| **本质** | 没有"传送带"，必须**手递手** | 有一条长度为 5 的"传送带" |
| **发送阻塞条件** | 对面没人接 → 立刻阻塞 | 传送带放满了 → 才阻塞 |
| **接收阻塞条件** | 对面没人发 → 立刻阻塞 | 传送带是空的 → 才阻塞 |
| **同步性** | 强同步：发送和接收必须**同时就绪**才能完成 | 异步解耦：只要缓冲没满/没空，双方互不等待 |
| **典型用途** | 信号通知、Goroutine 间严格同步 | 生产者-消费者模型、任务队列、削峰缓冲 |

```go
// 无缓冲示例：发送方会阻塞，直到接收方准备好
ch := make(chan int)
go func() { ch <- 42 }() // 如果没人读，这里会一直卡住
val := <-ch              // 此时发送方才被唤醒

// 有缓冲示例：缓冲没满就不阻塞
ch := make(chan int, 2)
ch <- 1  // 不阻塞，放入缓冲
ch <- 2  // 不阻塞，缓冲刚好满
// ch <- 3  // 这里才会阻塞！因为缓冲已满，没人取走数据
```

### 核心工作流程（按场景拆解）

#### 场景一：无缓冲 Channel

没有 `buf`，所有数据必须在发送方和接收方之间**直接传递**。

| 操作 | 情况 | 发生了什么 |
| :--- | :--- | :--- |
| `ch <- val` | `recvq` 里有人在等 | 直接把数据拷贝给那个等待的接收者，唤醒它。发送方不阻塞。 |
| `ch <- val` | `recvq` 没人 | 发送方被打包成 `sudog` 挂到 `sendq`，**睡觉等人来取**。 |
| `<-ch` | `sendq` 里有人在等 | 直接从那个等待的发送者手里拷贝数据，唤醒它。接收方不阻塞。 |
| `<-ch` | `sendq` 没人 | 接收方被打包成 `sudog` 挂到 `recvq`，**睡觉等人来送**。 |

> 核心规律：无缓冲 = 必须"握手"，一方先到就等另一方。

#### 场景二：有缓冲 Channel

有 `buf` 环形队列作为中间缓冲区。

| 操作 | 情况 | 发生了什么 |
| :--- | :--- | :--- |
| `ch <- val` | `recvq` 里有人在等 | 跳过缓冲，直接拷贝给接收者并唤醒（性能最高路径）。 |
| `ch <- val` | 没人等，`buf` 没满 | 数据拷入 `buf` 尾部，发送方继续执行，不阻塞。 |
| `ch <- val` | 没人等，`buf` 满了 | 发送方挂到 `sendq` 睡觉，等有人取走数据后被唤醒。 |
| `<-ch` | `sendq` 有人等（说明 `buf` 一定是满的） | 从 `buf` 头部取一个数据给接收方，再把 `sendq` 里等待的发送者的数据补入 `buf` 尾部，唤醒发送者。 |
| `<-ch` | `sendq` 没人，`buf` 有数据 | 直接从 `buf` 头部取出数据。 |
| `<-ch` | `sendq` 没人，`buf` 也空 | 接收方挂到 `recvq` 睡觉，等有人发数据后被唤醒。 |

> 核心规律：有缓冲 = 先往传送带上放，放满了才排队等。

---

## 3. Sync 常用机制与适用场景

| 工具 | 作用说明 | 最佳适用场景 |
| :--- | :--- | :--- |
| **`sync.Mutex`**<br>(互斥锁) | **独占锁**。同一时刻只能有一个 Goroutine 拿到锁进行读写。 | 保护会被并发读写的单一变量或状态（如：全局并发计数器、状态标志位）。 |
| **`sync.RWMutex`**<br>(读写锁) | **读共享，写独占**。多 Goroutine 可同时读，但写时全部阻塞。 | **读多写少**的并发场景（如：内存缓存的读取、全局配置的读取）。性能远高于 Mutex。 |
| **`sync.WaitGroup`**<br>(等待组) | **并发计数器**。`Add` 增加计数，`Done` 减少计数，`Wait` 阻塞直到归零。 | 主 Goroutine 需要**等待一组子任务全部执行完毕**后才能继续往下走的操作（如：并行下载分片后合并数据）。 |

---

## 4. 三者协同：Worker Pool (工作池模式)

*   **场景**：并发处理 5 个任务，但最多只允许 3 个 Goroutine 同时运行。
*   **三者分工**：Goroutine 是工人，Channel 是任务队列和结果队列，WaitGroup 负责等所有工人下班。

```go
package main

import (
	“fmt”
	“sync”
	“time”
)

func worker(id int, jobs <-chan int, results chan<- int, wg *sync.WaitGroup) {
	defer wg.Done()
	for j := range jobs {
		fmt.Printf(“Worker %d 开始处理任务 %d\n”, id, j)
		time.Sleep(time.Second) // 模拟耗时 1 秒
		result := j * 2
		fmt.Printf(“Worker %d 完成任务 %d → 结果 %d\n”, id, j, result)
		results <- result
	}
}

func main() {
	const numJobs = 5
	const numWorkers = 3

	jobs := make(chan int, numJobs)
	results := make(chan int, numJobs)
	var wg sync.WaitGroup

	// 1. 启动 3 个 Worker
	for w := 1; w <= numWorkers; w++ {
		wg.Add(1)
		go worker(w, jobs, results, &wg)
	}

	// 2. 塞入 5 个任务
	for j := 1; j <= numJobs; j++ {
		jobs <- j
	}
	close(jobs)

	// 3. 等待所有 Worker 完工后关闭 results
	go func() {
		wg.Wait()
		close(results)
	}()

	// 4. 收集结果
	for r := range results {
		fmt.Println(“收到结果:”, r)
	}
}
```

### 运行结果示例（实际顺序随机，但逻辑一定如下）

```
// ===== 第 1 秒：3 个 Worker 同时抢到任务 1、2、3 并开始处理 =====
Worker 1 开始处理任务 1
Worker 2 开始处理任务 2
Worker 3 开始处理任务 3

// ===== 1 秒后：首批 3 个任务完成，结果产出 =====
Worker 1 完成任务 1 → 结果 2
Worker 2 完成任务 2 → 结果 4
Worker 3 完成任务 3 → 结果 6
收到结果: 2
收到结果: 4
收到结果: 6

// ===== 紧接着：空闲的 Worker 继续抢剩余的任务 4、5（只有 2 个任务，所以 1 个 Worker 闲置）=====
Worker 1 开始处理任务 4
Worker 2 开始处理任务 5

// ===== 再过 1 秒：最后 2 个任务完成 =====
Worker 1 完成任务 4 → 结果 8
Worker 2 完成任务 5 → 结果 10
收到结果: 8
收到结果: 10
```

**执行流程逐步拆解**：

1. `main` 启动 3 个 Worker Goroutine，它们都在 `for j := range jobs` 处等待任务。
2. `main` 往 `jobs` 塞入任务 1~5，3 个 Worker 各抢到一个（比如 1、2、3），开始并行处理。此时任务 4、5 留在 `jobs` 缓冲区里。
3. 约 1 秒后，3 个 Worker 各自完成手头任务，把结果（2、4、6）发到 `results`，然后立刻回到 `range jobs` 抢下一个任务。
4. Worker 1 抢到任务 4，Worker 2 抢到任务 5，Worker 3 发现 `jobs` 已空且已 `close`，`range` 循环退出，调用 `wg.Done()`。
5. 再过 1 秒，Worker 1 和 2 完成任务 4、5，结果（8、10）发到 `results`，然后也退出循环并 `wg.Done()`。
6. 后台的 `wg.Wait()` 检测到计数归零，执行 `close(results)`。
7. `main` 中的 `for r := range results` 读完所有结果后循环结束，程序退出。

> **关键点**：Worker 的编号和抢到哪个任务是完全随机的（取决于 Go 调度器），但同一时刻最多只有 3 个任务在并行处理——这就是 Worker Pool 的并发控制效果。