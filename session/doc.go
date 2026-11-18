// Package session 是 GUI 使用的无界面播放会话。
//
// 它复用 app、store 与 player.Controller：桌面先拿实例锁再打开数据库，
// 恢复队列时不自动播放。解析在可取消任务里执行，控制器操作串行。
// TUI 仍使用自己的编排，本包不反向依赖终端界面。
//
// 三个标识不能混用：控制器请求号（player.State.Request）、JSON-RPC id、
// 以及本包分配的播放代次 playback id。
package session
