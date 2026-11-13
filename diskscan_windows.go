//go:build windows

package main

// Windows-часть дискового сканера: быстрое перечисление каталогов через
// FindFirstFileExW (как в TreeSize), опустошение корзины через
// SHEmptyRecycleBinW и помощник «Оптимизации системы».

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── быстрое перечисление каталога ───────────────────────────────────────

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procFindFirstFileEx = kernel32.NewProc("FindFirstFileExW")
	procFindNextFile    = kernel32.NewProc("FindNextFileW")
	procFindClose       = kernel32.NewProc("FindClose")
)

const (
	findExInfoBasic   = 1 // FindExInfoBasic: без коротких имён 8.3 — заметно быстрее
	findExLargeFetch  = 2 // FIND_FIRST_EX_LARGE_FETCH: крупный батч за один вызов
	fileAttrDirectory = 0x10
	fileAttrReparse   = 0x400
)

// readDirEntries — перечисление каталога одним потоком FindFirstFile/Next:
// размер и тип берутся прямо из WIN32_FIND_DATA (ноль лишних вызовов на
// файл), без сортировки, с крупным kernel-буфером — так же, как это
// делают TreeSize/WizTree. Длинные пути (>MAX_PATH) API не отдаёт —
// ограничение Win32-перечисления, одинаковое для всех таких сканеров.
func readDirEntries(path string) ([]walkEntry, error) {
	pattern, err := syscall.UTF16PtrFromString(path + `\*`)
	if err != nil {
		return nil, err
	}
	var fd syscall.Win32finddata
	h, _, callErr := procFindFirstFileEx.Call(
		uintptr(unsafe.Pointer(pattern)),
		uintptr(findExInfoBasic),
		uintptr(unsafe.Pointer(&fd)),
		0, // FindExSearchNameMatch
		0,
		uintptr(findExLargeFetch),
	)
	runtime.KeepAlive(pattern)
	if h == uintptr(windows.InvalidHandle) {
		return nil, callErr
	}
	defer procFindClose.Call(h)

	out := make([]walkEntry, 0, 64)
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if name != "." && name != ".." {
			attrs := fd.FileAttributes
			out = append(out, walkEntry{
				name: name,
				size: int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow),
				dir:  attrs&fileAttrDirectory != 0,
				link: attrs&fileAttrReparse != 0,
			})
		}
		r1, _, _ := procFindNextFile.Call(h, uintptr(unsafe.Pointer(&fd)))
		if r1 == 0 {
			break // ERROR_NO_MORE_FILES — каталог кончился
		}
	}
	return out, nil
}

var (
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procEmptyRecycleBin = shell32.NewProc("SHEmptyRecycleBinW")
)

const (
	sherbNoConfirmation = 0x1
	sherbNoProgressUI   = 0x2
	sherbNoSound        = 0x4
)

func emptyTrash() error {
	// пустая корзина и занятые файлы возвращают HRESULT — не считаем
	// это ошибкой: попытка была, результат увидим при пересканировании
	_, _, _ = procEmptyRecycleBin.Call(
		0, 0,
		sherbNoConfirmation|sherbNoProgressUI|sherbNoSound,
	)
	return nil
}

// ── помощник «Оптимизации системы» ──────────────────────────────────────

// hideConsole — дочерние консольные утилиты (dism, reg, powershell) из
// GUI-процесса иначе открывают своё чёрное окно поверх всего экрана.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// runOptimizeHelper — тело SpeedTestTray.exe -optimize: процесс запускается
// с правами администратора (UAC из дашборда), выполняет шаги очистки
// официальными утилитами Microsoft и докладывает ход в лог-файл маркерами
// #step/#pct/#log/#done/#error — дашборд читает их и рисует в UI.
func runOptimizeHelper(logPath string, resetBase bool) {
	f, err := os.Create(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	step := func(n int, title string) {
		fmt.Fprintf(w, "#step %d\n#log %s\n", n, title)
		w.Flush()
	}
	logln := func(s string) {
		fmt.Fprintf(w, "#log %s\n", s)
		w.Flush()
	}
	fail := func(msg string) {
		fmt.Fprintf(w, "#error %s\n", msg)
		w.Flush()
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		fail("нет прав администратора — перезапустите оптимизацию")
		return
	}

	// кооперативная отмена: дашборд создаёт stop-файл, помощник замечает и
	// сам убивает свой dism (убить elevated-процесс извне нельзя)
	stopFile := logPath + ".stop"
	_ = os.Remove(stopFile)
	defer os.Remove(stopFile)
	stopCh := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(stopFile); err == nil {
				close(stopCh)
				return
			}
			time.Sleep(400 * time.Millisecond)
		}
	}()

	// шаг 1: анализ ДО очистки — ориентир, сколько освободилось
	step(1, "Анализ хранилища компонентов (до очистки)")
	runDism(w, logln, []string{"/Online", "/Cleanup-Image", "/AnalyzeComponentStore"}, false, stopCh)

	// шаг 2: очистка хранилища компонентов — самая долгая часть
	step(2, "Очистка компонентов DISM — обычно 10–30 минут, не закрывайте приложение")
	dismArgs := []string{"/Online", "/Cleanup-Image", "/StartComponentCleanup"}
	if resetBase {
		// ResetBase: после него установленные обновления нельзя удалить
		dismArgs = append(dismArgs, "/ResetBase")
	}
	select {
	case <-stopCh:
	default:
	}
	if !runDism(w, logln, dismArgs, true, stopCh) {
		select {
		case <-stopCh:
			fail("#cancelled")
		default:
			fail("DISM завершился с ошибкой — очистка остановлена")
		}
		return
	}

	// шаг 3: очистка временных файлов cleanmgr. В отличие от ручного
	// sageset галки выставляем кодом — только безопасные категории,
	// ничего про «Загрузки» и прочее пользовательское
	select {
	case <-stopCh:
		fail("#cancelled")
		return
	default:
	}
	step(3, "Очистка временных файлов Windows (cleanmgr)")
	const caches = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\VolumeCaches`
	for _, cat := range cleanmgrSafe {
		reg := exec.Command("reg", "add", caches+`\`+cat,
			"/v", "StateFlags0001", "/t", "REG_DWORD", "/d", "2", "/f")
		hideConsole(reg)
		reg.Run()
	}
	clean := exec.Command("cleanmgr", "/sagerun:1")
	hideConsole(clean)
	if err := clean.Run(); err != nil {
		logln("cleanmgr недоступен — шаг пропущен")
	}

	// шаг 4: анализ ПОСЛЕ — сравнить с шагом 1
	step(4, "Анализ хранилища компонентов (после очистки)")
	runDism(w, logln, []string{"/Online", "/Cleanup-Image", "/AnalyzeComponentStore"}, false, stopCh)

	logln("Готово. Рекомендуется перезагрузка Windows.")
	fmt.Fprintln(w, "#done")
	w.Flush()
}

// cleanmgrSafe — категории cleanmgr, безопасные для автоматической очистки.
var cleanmgrSafe = []string{
	"Temporary Files", "Temporary Setup Files", "Thumbnail Cache",
	"Delivery Optimization Files", "Windows Error Reporting Files",
	"DirectX Shader Cache", "Internet Cache Files", "Old ChkDsk Files",
}

// runDism — запуск dism с потоковым логом; у долгих операций процент
// выдирается из строк прогресса вида «[===  37.5%  ]». При отмене
// (закрытие stopCh) процесс dism убивается — результат «отменено».
func runDism(w *bufio.Writer, logln func(string), args []string, progress bool, stopCh chan struct{}) bool {
	cmd := exec.Command("dism", args...)
	hideConsole(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		logln("dism: " + err.Error())
		return false
	}
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if progress {
				if m := re.FindStringSubmatch(line); m != nil {
					var pct float64
					fmt.Sscanf(m[1], "%f", &pct)
					fmt.Fprintf(w, "#pct %d\n", int(pct))
					w.Flush()
				}
			}
			logln(line)
		}
	}()
	waitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait() // дисмовский stdout больше никто не пишет
		pw.Close()        // после выхода процесса — сканер увидит EOF
		waitErr <- err
	}()
	select {
	case err := <-waitErr:
		<-scanDone
		if err == nil {
			return true
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3010 {
			// 3010 = «успешно, требуется перезагрузка»
			logln("DISM: требуется перезагрузка Windows (код 3010)")
			return true
		}
		if ee, ok := err.(*exec.ExitError); ok {
			logln(fmt.Sprintf("DISM завершился с кодом %d", ee.ExitCode()))
		}
		return false
	case <-stopCh:
		_ = cmd.Process.Kill()
		<-waitErr
		<-scanDone
		logln("DISM остановлен по запросу пользователя")
		return false
	}
}
