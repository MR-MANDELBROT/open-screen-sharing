param(
    [switch]$Animate,
    [switch]$Audio,
    [switch]$Maximize,
    [switch]$Stress,
    [ValidateRange(1, 1000)][int]$FrameIntervalMs = 15
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class RDPFixtureDisplayMetrics
{
    [StructLayout(LayoutKind.Sequential)]
    private struct Rect { public int Left, Top, Right, Bottom; }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct MonitorInfo
    {
        public uint Size;
        public Rect Monitor, Work;
        public uint Flags;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string Device;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct DevMode
    {
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string DeviceName;
        public ushort SpecVersion, DriverVersion, Size, DriverExtra;
        public uint Fields;
        public int PositionX, PositionY;
        public uint DisplayOrientation, DisplayFixedOutput;
        public short Color, Duplex, YResolution, TTOption, Collate;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)] public string FormName;
        public ushort LogPixels;
        public uint BitsPerPel, PelsWidth, PelsHeight, DisplayFlags, DisplayFrequency;
        public uint IcmMethod, IcmIntent, MediaType, DitherType, Reserved1, Reserved2;
        public uint PanningWidth, PanningHeight;
    }

    [DllImport("user32.dll")]
    private static extern uint GetDpiForWindow(IntPtr window);
    [DllImport("user32.dll")]
    private static extern uint GetDpiForSystem();
    [DllImport("user32.dll")]
    private static extern IntPtr GetWindowDpiAwarenessContext(IntPtr window);
    [DllImport("user32.dll")]
    private static extern int GetAwarenessFromDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")]
    private static extern IntPtr MonitorFromWindow(IntPtr window, uint flags);
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool GetMonitorInfo(IntPtr monitor, ref MonitorInfo info);
    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool GetWindowRect(IntPtr window, out Rect rect);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern bool EnumDisplaySettings(string device, int mode, ref DevMode settings);
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateWindowEx(uint exStyle, string className, string title,
        uint style, int x, int y, int width, int height, IntPtr parent, IntPtr menu,
        IntPtr instance, IntPtr parameter);
    [DllImport("user32.dll")]
    private static extern bool DestroyWindow(IntPtr window);

    public static Dictionary<string, object> Read(IntPtr window)
    {
        var result = new Dictionary<string, object>();
        result["fixtureWindowDpi"] = GetDpiForWindow(window);
        result["fixtureSystemDpi"] = GetDpiForSystem();
        result["fixtureAwareness"] = GetAwarenessFromDpiAwarenessContext(
            GetWindowDpiAwarenessContext(window));

        IntPtr previous = SetThreadDpiAwarenessContext(new IntPtr(-4));
        if (previous == IntPtr.Zero)
            throw new Win32Exception(Marshal.GetLastWin32Error());
        IntPtr probe = IntPtr.Zero;
        try
        {
            var monitor = new MonitorInfo();
            monitor.Size = (uint)Marshal.SizeOf(typeof(MonitorInfo));
            if (!GetMonitorInfo(MonitorFromWindow(window, 2), ref monitor))
                throw new Win32Exception(Marshal.GetLastWin32Error());
            result["monitorDevice"] = monitor.Device;
            result["monitorPixelWidth"] = monitor.Monitor.Right - monitor.Monitor.Left;
            result["monitorPixelHeight"] = monitor.Monitor.Bottom - monitor.Monitor.Top;
            Rect bounds;
            if (!GetWindowRect(window, out bounds))
                throw new Win32Exception(Marshal.GetLastWin32Error());
            result["windowPixelWidth"] = bounds.Right - bounds.Left;
            result["windowPixelHeight"] = bounds.Bottom - bounds.Top;

            probe = CreateWindowEx(0x08000080, "STATIC", "", 0x80000000,
                monitor.Monitor.Left + (monitor.Monitor.Right - monitor.Monitor.Left) / 2,
                monitor.Monitor.Top + (monitor.Monitor.Bottom - monitor.Monitor.Top) / 2,
                1, 1, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
            if (probe == IntPtr.Zero)
                throw new Win32Exception(Marshal.GetLastWin32Error());
            uint dpi = GetDpiForWindow(probe);
            if (dpi == 0)
                throw new InvalidOperationException("The DPI probe returned zero DPI.");
            result["monitorEffectiveDpi"] = dpi;
            result["monitorScalePercent"] = dpi * 100.0 / 96;
            result["monitorLogicalWidthAt96Dpi"] =
                (monitor.Monitor.Right - monitor.Monitor.Left) * 96.0 / dpi;
            result["monitorLogicalHeightAt96Dpi"] =
                (monitor.Monitor.Bottom - monitor.Monitor.Top) * 96.0 / dpi;

            // EnumDisplaySettings always returns physical pixels, regardless of awareness.
            var mode = new DevMode();
            mode.Size = (ushort)Marshal.SizeOf(typeof(DevMode));
            bool hasMode = EnumDisplaySettings(monitor.Device, -1, ref mode);
            result["displayModeAvailable"] = hasMode;
            if (hasMode)
            {
                result["displayPixelWidth"] = mode.PelsWidth;
                result["displayPixelHeight"] = mode.PelsHeight;
            }
        }
        finally
        {
            if (probe != IntPtr.Zero)
                DestroyWindow(probe);
            SetThreadDpiAwarenessContext(previous);
        }
        return result;
    }
}
'@
if ($Stress) {
    Add-Type -ReferencedAssemblies System.Windows.Forms,System.Drawing -TypeDefinition @'
using System;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
using System.Windows.Forms;

public sealed class RDPFixtureStressForm : Form
{
    public RDPFixtureStressForm()
    {
        SetStyle(ControlStyles.AllPaintingInWmPaint | ControlStyles.UserPaint |
            ControlStyles.OptimizedDoubleBuffer, true);
    }
}

public sealed class RDPFixtureStressPainter : IDisposable
{
    private Bitmap bitmap;
    private int[] pixels;

    public void Draw(Graphics graphics, int width, int height, long frame)
    {
        if (width <= 0 || height <= 0)
            return;
        if (bitmap == null || bitmap.Width != width || bitmap.Height != height)
        {
            Dispose();
            bitmap = new Bitmap(width, height, PixelFormat.Format32bppPArgb);
            pixels = new int[checked(width * height)];
        }
        int phase = (int)(frame & 255);
        int offset = 0;
        for (int y = 0; y < height; y++)
        {
            int green = ((y + phase * 3) & 255) << 8;
            int row = (y >> 5) ^ (phase >> 3);
            for (int x = 0; x < width; x++)
            {
                int checker = ((x >> 5) ^ row) & 1;
                int red = ((x + phase + checker * 64) & 255) << 16;
                int blue = ((x + y) / 2 + phase * 7 + checker * 96) & 255;
                pixels[offset++] = unchecked((int)0xff000000) | red | green | blue;
            }
        }
        BitmapData data = bitmap.LockBits(new Rectangle(0, 0, width, height),
            ImageLockMode.WriteOnly, PixelFormat.Format32bppPArgb);
        try
        {
            if (data.Stride == width * 4)
                Marshal.Copy(pixels, 0, data.Scan0, pixels.Length);
            else
                for (int y = 0; y < height; y++)
                    Marshal.Copy(pixels, y * width, IntPtr.Add(data.Scan0, y * data.Stride), width);
        }
        finally
        {
            bitmap.UnlockBits(data);
        }
        graphics.DrawImageUnscaled(bitmap, 0, 0);
    }

    public void Dispose()
    {
        if (bitmap != null)
            bitmap.Dispose();
        bitmap = null;
        pixels = null;
    }
}
'@
}
$directory = Join-Path $env:TEMP 'oss-rdp-fixture'
New-Item -ItemType Directory -Path $directory -Force | Out-Null
$log = Join-Path $directory 'events.jsonl'
function Record($kind, $value) {
    [IO.File]::AppendAllText($log, (@{ event=$kind; value=$value; time=[DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds() } | ConvertTo-Json -Compress) + "`n", [Text.UTF8Encoding]::new($false))
}
function RecordDisplayMetrics($reason) {
    if (-not $form.IsHandleCreated -or $form.IsDisposed) { return }
    try {
        $metrics = [RDPFixtureDisplayMetrics]::Read($form.Handle)
        $metrics['reason'] = $reason
        $metrics['windowLayoutWidth'] = $form.Width
        $metrics['windowLayoutHeight'] = $form.Height
        $metrics['clientLayoutWidth'] = $form.ClientSize.Width
        $metrics['clientLayoutHeight'] = $form.ClientSize.Height
        Record 'display-metrics' $metrics
    } catch {
        Record 'display-metrics-error' $_.Exception.Message
    }
}
$stressPainter = $null
if ($Stress) {
    $form = New-Object RDPFixtureStressForm
    $stressPainter = New-Object RDPFixtureStressPainter
} else {
    $form = New-Object Windows.Forms.Form
}
$form.Text = 'Open Screen Sharing RDP validation'
$form.StartPosition = 'CenterScreen'
$form.Size = New-Object Drawing.Size(960, 700)
if ($Maximize) { $form.WindowState = [Windows.Forms.FormWindowState]::Maximized }
$form.KeyPreview = $true
$form.BackColor = [Drawing.Color]::FromArgb(32, 64, 96)
$box = New-Object Windows.Forms.TextBox
$box.Multiline = $true
$box.AcceptsReturn = $true
$box.Font = New-Object Drawing.Font('Segoe UI', 20)
$box.Dock = 'Bottom'
$box.Height = 180
$box.Text = ''
$form.Controls.Add($box)
$box.add_TextChanged({ Record 'text' $box.Text })
$form.add_KeyDown({
    param($s,$e)
    Record 'key' @{code=$e.KeyCode.ToString();control=$e.Control;shift=$e.Shift;alt=$e.Alt}
    if ($e.Control -and $e.Shift -and -not $e.Alt) {
        switch ($e.KeyCode) {
            ([Windows.Forms.Keys]::C) {
                [Windows.Forms.Clipboard]::SetText('OSS remote clipboard ' + [char]0x2713 + ' ' + [char]0x4E2D + [char]0x6587)
                Record 'clipboard' 'set'
                $e.SuppressKeyPress = $true
            }
            ([Windows.Forms.Keys]::D) {
                RecordDisplayMetrics 'requested'
                $e.SuppressKeyPress = $true
            }
            ([Windows.Forms.Keys]::R) {
                $box.Clear()
                Record 'reset' 'text'
                $e.SuppressKeyPress = $true
            }
            ([Windows.Forms.Keys]::P) {
                if ($player) {
                    $player.Stop()
                    $player.PlayLooping()
                    Record 'audio' 'restarted'
                } else {
                    Record 'audio' 'disabled'
                }
                $e.SuppressKeyPress = $true
            }
            ([Windows.Forms.Keys]::F) {
                if ($timer.Enabled) { $timer.Stop() } else { $timer.Start() }
                Record 'animation' $timer.Enabled
                $e.SuppressKeyPress = $true
            }
        }
    }
})
$form.add_MouseDown({param($s,$e) Record 'pointer' @{x=$e.X;y=$e.Y;button=$e.Button.ToString()} })
$form.add_MouseWheel({param($s,$e) Record 'wheel' $e.Delta })
$form.add_Resize({ Record 'window-size' @{width=$form.Width;height=$form.Height}; RecordDisplayMetrics 'resize' })
$script:frame = 0
$script:paintedFrames = 0
$form.add_Paint({
    param($s,$e)
    $g = $e.Graphics
    $width = [Math]::Max(1, $form.ClientSize.Width)
    $height = [Math]::Max(1, $form.ClientSize.Height - $box.Height)
    if ($Stress) {
        $stressPainter.Draw($g, $width, $height, $script:frame)
        $script:paintedFrames++
        if (($script:paintedFrames % 60) -eq 0) {
            Record 'painted-frame' @{count=$script:paintedFrames;frame=$script:frame;width=$width;height=$height}
        }
        return
    }
    $colors = @([Drawing.Color]::Red, [Drawing.Color]::Lime, [Drawing.Color]::Blue, [Drawing.Color]::White)
    for ($i=0; $i -lt 4; $i++) {
        $brush = New-Object Drawing.SolidBrush($colors[$i])
        $g.FillRectangle($brush, [int]($i*$width/4), 0, [int]($width/4), 100)
        $brush.Dispose()
    }
    $x = ($script:frame * 13) % $width
    $g.FillRectangle([Drawing.Brushes]::Gold, $x, 130, 96, 96)
    $g.DrawString("RDP frame $script:frame", $box.Font, [Drawing.Brushes]::White, 30, 260)
})
$timer = New-Object Windows.Forms.Timer
$timer.Interval = $FrameIntervalMs
$timer.add_Tick({ $script:frame++; $form.Invalidate(); if (($script:frame % 60) -eq 0) { Record 'frame' $script:frame } })
if ($Animate -or $Stress) { $timer.Start() }
$player = $null
if ($Audio) {
    $path = Join-Path $directory 'tone.wav'
    $sampleRate=44100; $samples=44100
    $stream=[IO.File]::Open($path,[IO.FileMode]::Create)
    $writer=New-Object IO.BinaryWriter($stream)
    $writer.Write([Text.Encoding]::ASCII.GetBytes('RIFF'));$writer.Write([int](36+$samples*2));$writer.Write([Text.Encoding]::ASCII.GetBytes('WAVEfmt '));$writer.Write([int]16);$writer.Write([int16]1);$writer.Write([int16]1);$writer.Write([int]$sampleRate);$writer.Write([int]($sampleRate*2));$writer.Write([int16]2);$writer.Write([int16]16);$writer.Write([Text.Encoding]::ASCII.GetBytes('data'));$writer.Write([int]($samples*2))
    for ($i=0; $i -lt $samples; $i++) { $writer.Write([int16](2000*[Math]::Sin(2*[Math]::PI*440*$i/$sampleRate))) }
    $writer.Dispose();$stream.Dispose()
    $player = New-Object Media.SoundPlayer($path)
    $player.PlayLooping()
}
$form.add_Shown({ $box.Focus(); Record 'ready' @{user=$env:USERNAME;screenWidth=[Windows.Forms.Screen]::PrimaryScreen.Bounds.Width;screenHeight=[Windows.Forms.Screen]::PrimaryScreen.Bounds.Height;stress=[bool]$Stress;animated=$timer.Enabled;interval=$timer.Interval}; RecordDisplayMetrics 'ready' })
try { [Windows.Forms.Application]::Run($form) } finally { $timer.Stop(); $timer.Dispose(); if ($player) { $player.Stop();$player.Dispose() }; $form.Dispose(); if ($stressPainter) { $stressPainter.Dispose() }; Record 'closed' $null }
