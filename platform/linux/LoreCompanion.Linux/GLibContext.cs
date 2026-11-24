using System.Collections.Concurrent;
using System.Runtime.InteropServices;

namespace LoreCompanion;

/// <summary>Runs continuations on the GTK main loop, as WPF's dispatcher does on Windows: an
/// <c>await</c> in the operation session comes back to the thread that may touch the windows.</summary>
internal sealed unsafe class GLibContext : SynchronizationContext
{
    private static readonly ConcurrentQueue<(SendOrPostCallback Callback, object? State)> Queue = new();

    public override void Post(SendOrPostCallback callback, object? state)
    {
        Queue.Enqueue((callback, state));
        Native.g_idle_add(&Drain, IntPtr.Zero);
    }

    public override void Send(SendOrPostCallback callback, object? state) =>
        throw new NotSupportedException("The GTK loop runs work only asynchronously.");

    public override SynchronizationContext CreateCopy() => this;

    [UnmanagedCallersOnly]
    private static int Drain(IntPtr data)
    {
        if (Queue.TryDequeue(out var work))
        {
            try { work.Callback(work.State); }
            catch (Exception error) { OperationSession.Log($"main loop work failed: {error}"); }
        }
        return 0; // G_SOURCE_REMOVE: each post adds its own idle call
    }
}
