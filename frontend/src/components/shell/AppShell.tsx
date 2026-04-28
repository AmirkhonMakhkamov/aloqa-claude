import { Rail } from "@/components/shell/Rail";
import { ContextSidebar } from "@/components/shell/ContextSidebar";

/*
 * The standard authenticated shell: rail (64px, dark) + contextual sidebar
 * (248px, dark) + main content pane. Every authenticated route inside the
 * /w/[wsId] subtree renders inside this shell.
 *
 * Main content is deliberately a flex column so child pages can freely use
 * their own header bars (ChannelHeader, CallHeader, etc.) without having
 * to fight the shell for vertical space.
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full w-full">
      <Rail />
      <ContextSidebar />
      <main className="flex min-w-0 flex-1 flex-col overflow-hidden bg-app">
        {children}
      </main>
    </div>
  );
}
