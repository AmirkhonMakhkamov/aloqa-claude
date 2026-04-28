import { redirect } from "next/navigation";

export default async function AdminIndex({
  params,
}: {
  params: Promise<{ wsId: string }>;
}) {
  const { wsId } = await params;
  redirect(`/w/${wsId}/admin/members`);
}
