"use client";

import { useEffect, useRef } from "react";
import type { FormEvent } from "react";
import Link from "next/link";
import { SignOutButton } from "@clerk/nextjs";
import type { Application, ApplicationFilters, ApplicationPage, Task, TaskPage } from "@/lib/api";
import { ApplicationCaptureForm } from "./ApplicationCaptureForm";
import { ApplicationEditForm } from "./ApplicationEditForm";
import { ApplicationFilterBar } from "./ApplicationFilterBar";
import { ApplicationList } from "./ApplicationList";
import { NotificationControl } from "./NotificationControl";
import { TaskForm } from "./TaskForm";
import { TodayPanel } from "./TodayPanel";
import { ThemeControl } from "./ThemeControl";

export type TaskDraft = { application: Application; task: Task | null };

type Props = {
  email: string;
  result: ApplicationPage;
  filters: ApplicationFilters;
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  loading: boolean;
  loadError: boolean;
  saving: boolean;
  busyId: string;
  editingApplication: Application | null;
  taskPage: TaskPage;
  taskPageIndex: number;
  taskHasPrevious: boolean;
  taskHasNext: boolean;
  taskLoading: boolean;
  taskLoadError: boolean;
  tasksShowAll: boolean;
  taskBusyId: string;
  taskDraft: TaskDraft | null;
  error: string;
  notice: string;
  notificationEnabled: boolean | null;
  notificationLoading: boolean;
  notificationSaving: boolean;
  notificationError: string;
  notificationNotice: string;
  onNotificationChange: (enabled: boolean) => void;
  onCreate: (event: FormEvent<HTMLFormElement>) => void;
  onStatusChange: (application: Application, status: Application["status"]) => void;
  onEdit: (application: Application) => void;
  onCancelEdit: () => void;
  onSaveEdit: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onRemove: (application: Application) => void;
  onAddTask: (application: Application) => void;
  onApplyFilters: (filters: ApplicationFilters) => void;
  onClearFilters: () => void;
  onPrevious: () => void;
  onNext: () => void;
  onToggleFinished: () => void;
  onTaskPrevious: () => void;
  onTaskNext: () => void;
  onRetryApplications: () => void;
  onRetryTasks: () => void;
  onEditTask: (task: Task, application?: Application) => void;
  onSaveTask: (event: FormEvent<HTMLFormElement>, id?: string) => void;
  onCancelTaskEdit: () => void;
  onCompleteTask: (task: Task) => void;
  onCancelTask: (task: Task) => void;
  onExport: (task: Task) => void;
};

export function ApplicationDashboard({
  email, result, filters, page, hasPrevious, hasNext, loading, loadError, saving, busyId, editingApplication,
  taskPage, taskPageIndex, taskHasPrevious, taskHasNext, taskLoading, taskLoadError, tasksShowAll, taskBusyId, taskDraft, error, notice,
  notificationEnabled, notificationLoading, notificationSaving, notificationError, notificationNotice, onNotificationChange,
  onCreate, onStatusChange, onEdit, onCancelEdit, onSaveEdit, onRemove, onAddTask, onApplyFilters, onClearFilters,
  onPrevious, onNext, onToggleFinished, onTaskPrevious, onTaskNext, onRetryApplications, onRetryTasks, onEditTask, onSaveTask, onCancelTaskEdit, onCompleteTask, onCancelTask, onExport,
}: Props) {
  // Counts come from the API for every matching row, never from the current
  // page, so the summary stays honest while paging or filtering. The total
  // counts every stage; saved and applied remain as separate summary rows.
  const counts = result.counts;
  const total = [
    counts.saved, counts.applied, counts.interviewing, counts.offer,
    counts.accepted, counts.rejected, counts.withdrawn,
  ].reduce((sum, value) => sum + value, 0);
  const lastEditingId = useRef("");
  const editingId = editingApplication?.id ?? "";
  const captureUnavailable = saving || !!editingApplication || !!taskDraft;

  useEffect(() => {
    if (editingId) {
      document.getElementById("edit-company")?.focus();
    } else if (lastEditingId.current) {
      document.getElementById(`edit-button-${lastEditingId.current}`)?.focus();
    }
    lastEditingId.current = editingId;
  }, [editingId]);

  return (
    <main className="page-shell">
      <header className="topbar">
        <Link className="wordmark" href="/" aria-label="Hireloop home"><span className="brand-mark">H</span>hireloop</Link>
        <div className="account-menu">
          <span className="account-email" title={email}>{email}</span>
          <ThemeControl />
          <SignOutButton><button className="button quiet" type="button">Sign out</button></SignOutButton>
        </div>
      </header>

      <section className="welcome-row">
        <div>
          <p className="eyebrow">Your job search</p>
          <h1>Applications</h1>
          <p className="muted">A calm place to keep track of every opportunity.</p>
        </div>
        <div className="summary" role="group" aria-label="Application counts">
          <div><strong>{total}</strong><span>Total</span></div>
          <div><strong>{result.counts.saved}</strong><span>Saved</span></div>
          <div><strong>{result.counts.applied}</strong><span>Applied</span></div>
        </div>
        <button
          className="button primary add-application-cta"
          type="button"
          disabled={captureUnavailable}
          aria-describedby={captureUnavailable ? "add-application-disabled" : undefined}
          onClick={() => {
            const company = document.getElementById("company");
            company?.scrollIntoView?.({ behavior: "smooth", block: "center" });
            company?.focus();
          }}
        >
          Add application
        </button>
        {captureUnavailable && <span id="add-application-disabled" className="visually-hidden">Close the current form before adding an application.</span>}
      </section>

      <NotificationControl
        enabled={notificationEnabled}
        loading={notificationLoading}
        saving={notificationSaving}
        error={notificationError}
        notice={notificationNotice}
        onChange={onNotificationChange}
      />

      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}

      <TodayPanel
        tasks={taskPage.items}
        applications={result.items}
        total={taskPage.total}
        page={taskPageIndex + 1}
        hasPrevious={taskHasPrevious}
        hasNext={taskHasNext}
        loading={taskLoading}
        loadError={taskLoadError}
        showAll={tasksShowAll}
        busyId={taskBusyId}
        onToggleFinished={onToggleFinished}
        onPrevious={onTaskPrevious}
        onNext={onTaskNext}
        onEdit={onEditTask}
        onComplete={onCompleteTask}
        onCancel={onCancelTask}
        onExport={onExport}
        onRetry={onRetryTasks}
      />

      <div className="workspace">
        <div className="list-column">
          <ApplicationFilterBar
            filters={filters}
            disabled={loading || saving}
            onApply={onApplyFilters}
            onClear={onClearFilters}
          />
          <ApplicationList
            result={result}
            filters={filters}
            page={page}
            hasPrevious={hasPrevious}
            hasNext={hasNext}
            loading={loading}
            loadError={loadError}
            busyId={busyId}
            saving={saving}
            editingId={editingApplication?.id ?? ""}
            onStatusChange={onStatusChange}
            onEdit={onEdit}
            onRemove={onRemove}
            onAddTask={onAddTask}
            onPrevious={onPrevious}
            onNext={onNext}
            onClearFilters={onClearFilters}
            onRetry={onRetryApplications}
          />
        </div>
        <ApplicationCaptureForm saving={saving} hidden={!!editingApplication || !!taskDraft} onSubmit={onCreate} />
        {editingApplication && (
          <ApplicationEditForm
            application={editingApplication}
            saving={saving}
            onSave={onSaveEdit}
            onCancel={onCancelEdit}
          />
        )}
        {taskDraft && (
          <TaskForm
            application={taskDraft.application}
            task={taskDraft.task}
            applications={result.items}
            saving={saving}
            onSave={onSaveTask}
            onCancel={onCancelTaskEdit}
          />
        )}
      </div>
      <footer className="footer-note">Hireloop keeps your job search organized, one next step at a time.</footer>
    </main>
  );
}
