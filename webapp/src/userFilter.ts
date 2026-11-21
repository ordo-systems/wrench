// The administrator's user filter of a view. The choice lives in the route query, so a filtered view is a
// shareable link: ?user=<email> for one user, ?user=* for everyone, no parameter for the caller ("Me").
import { computed, onMounted, ref, type ComputedRef, type Ref, type WritableComputedRef } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useClient, useIdentity, type Scope } from "./api";

export interface UserFilter {
  /** Only administrators get the filter; for everyone else user is always "". */
  admin: ComputedRef<boolean>;
  user: WritableComputedRef<Scope>;
  /** Other users' emails for the select, without the caller's own (that is "Me"). */
  emails: Ref<string[]>;
  me: ComputedRef<string>;
}

export function useUserFilter(): UserFilter {
  const route = useRoute();
  const router = useRouter();
  const client = useClient();
  const identity = useIdentity();
  const admin = computed(() => identity.value?.role === "admin");
  const me = computed(() => identity.value?.email ?? "");
  const emails = ref<string[]>([]);

  const user = computed<Scope>({
    get: () => (admin.value ? String(route.query.user ?? "") : ""),
    set: (v) => {
      const { user: _, ...rest } = route.query;
      void router.replace({ query: v ? { ...rest, user: v } : rest });
    },
  });

  onMounted(async () => {
    if (!admin.value) return;
    try {
      emails.value = (await client.users()).map((u) => u.email).filter((e) => e !== me.value);
    } catch {
      // the select still offers "Me" and "All users"
    }
  });

  return { admin, user, emails, me };
}
